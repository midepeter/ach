// Licensed to The Moov Authors under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. The Moov Authors licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package ach

import (
	"errors"
	"sort"
)

// Return a flattened version of a File, where batches with similar batch headers are consolidated.
//
// Two batches are eligible to be combined if:
//   - their headers match, excluding the batch number (which isn't used in return matching and reflects
//     the final composition of the file.)
//   - they don't contain any entries with common trace numbers, since trace numbers must be unique
//     within a batch.
func FlattenedFile(originalFile *File) (*File, error) {
	var originalBatches []mergeable

	// Convert batches and IAT batches to "mergeables" for consistent flattening logic
	for _, batch := range originalFile.Batches {
		originalBatches = append(originalBatches, mergeableBatcher{batch, nil})
	}
	for _, iatBatch := range originalFile.IATBatches {
		iab := iatBatch
		originalBatches = append(originalBatches, mergeableIATBatch{&iab, nil})
	}

	// Considering bigger batches first allows for the least number of flattened batches
	sort.Slice(originalBatches, func(i, j int) bool {
		return originalBatches[i].GetEntryCount() < originalBatches[j].GetEntryCount()
	})

	// Merge each original batch into a new batch
	newBatchesByHeader := map[string][]mergeable{}
	for _, batch := range originalBatches {
		var batchToMergeWith mergeable

		batchesWithMatchingHeader, found := newBatchesByHeader[batch.GetHeaderSignature()]
		if found {
			for _, batchWithMatchingHeader := range batchesWithMatchingHeader {
				if canMerge(batch, batchWithMatchingHeader) {
					batchToMergeWith = batchWithMatchingHeader
					break
				}
			}
		}

		if batchToMergeWith == nil {
			newBatchesByHeader[batch.GetHeaderSignature()] = append(newBatchesByHeader[batch.GetHeaderSignature()], batch.Copy())
		} else {
			batchToMergeWith.Consume(batch)
		}
	}

	// Create a new file containing each of our new batches
	newFile := originalFile.addFileHeaderData(NewFile())
	var allBatches []mergeable
	for _, batches := range newBatchesByHeader {
		allBatches = append(allBatches, batches...)
	}

	// Sort batches by original batch number to roughly maintain batch order in the flattened file
	sort.Slice(allBatches, func(i int, j int) bool { return allBatches[i].GetBatchNumber() < allBatches[j].GetBatchNumber() })

	for _, batch := range allBatches {
		batch.AddToFile(newFile)
	}

	if err := newFile.Create(); err != nil {
		return nil, err
	}
	if err := newFile.Validate(); err != nil {
		return nil, err
	}

	// Sanity checks; this is kind of a scary operation!
	if originalFile.Control.EntryAddendaCount != newFile.Control.EntryAddendaCount {
		return nil, errors.New("Flatten operation changed entry + addenda count.")
	}
	if originalFile.Control.TotalDebitEntryDollarAmountInFile != newFile.Control.TotalDebitEntryDollarAmountInFile {
		return nil, errors.New("Flatten operation changed total debit entry amount.")
	}
	if originalFile.Control.TotalCreditEntryDollarAmountInFile != newFile.Control.TotalCreditEntryDollarAmountInFile {
		return nil, errors.New("Flatten operation changed total credit entry amount.")
	}

	return newFile, nil
}

// Determine if two batches can be combined (ie, have the same header and no common trace numbers)
func canMerge(a mergeable, b mergeable) bool {
	traceNumbers := b.GetTraceNumbers()
	for traceNumber := range a.GetTraceNumbers() {
		_, found := traceNumbers[traceNumber]
		if found {
			return false
		}
	}

	return a.GetHeaderSignature() == b.GetHeaderSignature()
}

// Represents either a "normal" batch or an IAT batch
type mergeable interface {
	GetHeaderSignature() string
	GetTraceNumbers() map[string]bool
	Consume(mergeable)
	GetBatch() interface{}
	GetBatchNumber() int
	Copy() mergeable
	GetEntryCount() int
	AddToFile(*File)
}

type mergeableBatcher struct {
	batcher      Batcher
	traceNumbers map[string]bool
}

// Batch header excluding the batch number, which isn't important to preserve
func (b mergeableBatcher) GetHeaderSignature() string { return b.batcher.GetHeader().String()[:87] }
func (b mergeableBatcher) GetBatch() interface{}      { return b.batcher }
func (b mergeableBatcher) GetEntryCount() int         { return len(b.batcher.GetEntries()) }
func (b mergeableBatcher) GetBatchNumber() int        { return b.batcher.GetHeader().BatchNumber }

func (b mergeableBatcher) GetTraceNumbers() map[string]bool {
	if b.traceNumbers != nil {
		return b.traceNumbers
	}

	b.traceNumbers = map[string]bool{}
	for _, entry := range b.batcher.GetEntries() {
		b.traceNumbers[entry.TraceNumber] = true
	}

	return b.traceNumbers
}

func (m mergeableBatcher) Consume(mergeableToConsume mergeable) {
	batcherToConsume, ok := mergeableToConsume.GetBatch().(Batcher)
	if !ok {
		panic("Incompatible batch types")
	}

	// Keep the lower of the two batch numbers, to roughly maintain batch order in the flattened file
	if batcherToConsume.GetHeader().BatchNumber < m.batcher.GetHeader().BatchNumber {
		m.batcher.GetHeader().BatchNumber = batcherToConsume.GetHeader().BatchNumber
	}

	for _, entry := range batcherToConsume.GetEntries() {
		m.batcher.AddEntry(entry)
	}
	for _, advEntry := range batcherToConsume.GetADVEntries() {
		m.batcher.AddADVEntry(advEntry)
	}
}

func (m mergeableBatcher) Copy() mergeable {
	newBatcher, _ := NewBatch(m.batcher.GetHeader())
	newMergeable := mergeableBatcher{newBatcher, nil}
	newMergeable.Consume(m)

	return newMergeable
}

func (m mergeableBatcher) AddToFile(file *File) {
	// Sort entries by trace number
	sort.Slice(m.batcher.GetEntries(), func(i, j int) bool {
		return m.batcher.GetEntries()[i].TraceNumber < m.batcher.GetEntries()[j].TraceNumber
	})

	err := m.batcher.Create()
	if err != nil {
		panic(err)
	}

	m.batcher.GetHeader().BatchNumber = 0

	file.AddBatch(m.batcher)
}

type mergeableIATBatch struct {
	iatBatch     *IATBatch
	traceNumbers map[string]bool
}

// Batch header excluding the batch number, which isn't important to preserve
func (b mergeableIATBatch) GetHeaderSignature() string { return b.iatBatch.Header.String()[:87] }
func (b mergeableIATBatch) GetBatch() interface{}      { return *b.iatBatch }
func (b mergeableIATBatch) GetEntryCount() int         { return len(b.iatBatch.Entries) }
func (b mergeableIATBatch) GetBatchNumber() int        { return b.iatBatch.Header.BatchNumber }

func (b mergeableIATBatch) GetTraceNumbers() map[string]bool {
	if b.traceNumbers != nil {
		return b.traceNumbers
	}

	b.traceNumbers = map[string]bool{}
	for _, entry := range b.iatBatch.Entries {
		b.traceNumbers[entry.TraceNumber] = true
	}

	return b.traceNumbers
}

func (m mergeableIATBatch) Consume(mergeableToConsume mergeable) {
	batchToConsume, ok := mergeableToConsume.GetBatch().(IATBatch)
	if !ok {
		panic("Incompatible batch types")
	}

	// Keep the lower of the two batch numbers, to roughly maintain batch order in the flattened file
	if batchToConsume.Header.BatchNumber < m.iatBatch.Header.BatchNumber {
		m.iatBatch.Header.BatchNumber = batchToConsume.Header.BatchNumber
	}

	for _, entry := range batchToConsume.Entries {
		m.iatBatch.AddEntry(entry)
	}
}

func (m mergeableIATBatch) Copy() mergeable {
	newIATBatch := NewIATBatch(m.iatBatch.Header)
	newMergeable := mergeableIATBatch{&newIATBatch, nil}
	newMergeable.Consume(m)

	return newMergeable
}

func (m mergeableIATBatch) AddToFile(file *File) {
	// Sort entries by trace number
	sort.Slice(m.iatBatch.Entries, func(i, j int) bool {
		return m.iatBatch.Entries[i].TraceNumber < m.iatBatch.Entries[j].TraceNumber
	})

	err := m.iatBatch.Create()
	if err != nil {
		panic(err)
	}
	m.iatBatch.Header.BatchNumber = 0

	file.AddIATBatch(*m.iatBatch)
}

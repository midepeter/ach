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

package issues

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moov-io/ach"
	"github.com/moov-io/ach/cmd/achcli/describe"
	"github.com/moov-io/ach/server"
	"github.com/moov-io/base/log"

	kitlog "github.com/go-kit/log"
	"github.com/stretchr/testify/require"
)

func TestIssue1378(t *testing.T) {
	fd, err := os.Open(filepath.Join("testdata", "issue1378", "employers_test.txt"))
	require.NoError(t, err)
	t.Cleanup(func() { fd.Close() })

	address := "http://localhost:8080/files/create?bypassDestination=true&bypassOrigin=true&customTraceNumbers=true&bypassCompanyIdentificationMatch=true&allowInvalidAmounts=true&allowMissingFileControl=true&allowMissingFileHeader=true"
	req := httptest.NewRequest("POST", address, fd)

	// setup HTTP handler
	repo := server.NewRepositoryInMemory(0*time.Second, log.NewTestLogger())
	svc := server.NewService(repo)
	handler := server.MakeHTTPHandler(svc, repo, kitlog.NewNopLogger())

	// execute our HTTP request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	w.Flush()
	require.Equal(t, http.StatusOK, w.Code)

	// Parse the response
	var response struct {
		ID   string   `json:"id"`
		File ach.File `json:"file"`
	}
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	describe.File(os.Stdout, &response.File, nil)
}

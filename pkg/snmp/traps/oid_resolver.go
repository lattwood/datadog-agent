// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022-present Datadog, Inc.package traps

package traps

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"gopkg.in/yaml.v2"
)

const ddTrapDBFileNamePrefix string = "dd_traps_db"

type unmarshaller func(data []byte, v interface{}) error

// OIDResolver is a interface to get Trap and Variable metadata from OIDs
type OIDResolver interface {
	GetTrapMetadata(trapOID string) (TrapMetadata, error)
	GetVariableMetadata(trapOID string, varOID string) (VariableMetadata, error)
}

// NoOpOIDResolver is a dummy OIDResolver implementation that is unable to get any Trap or Variable metadata.
type NoOpOIDResolver struct{}

// GetTrapMetadata always return an error in this OIDResolver implementation
func (or NoOpOIDResolver) GetTrapMetadata(trapOID string) (TrapMetadata, error) {
	return TrapMetadata{}, fmt.Errorf("trap OID %s is not defined", trapOID)
}

// GetVariableMetadata always return an error in this OIDResolver implementation
func (or NoOpOIDResolver) GetVariableMetadata(trapOID string, varOID string) (VariableMetadata, error) {
	return VariableMetadata{}, fmt.Errorf("trap OID %s is not defined", trapOID)
}

// MultiFilesOIDResolver is an OIDResolver implementation that can be configured with multiple input files.
// Trap OIDs conflicts are resolved using the name of the source file in alphabetical order and by giving
// the less priority to Datadog's own database shipped with the agent.
// Variable OIDs conflicts are fully resolved by also looking at the trap OID. A given trap OID only
// exist in a single file (after the previous conflict resolution), meaning that we get the variable
// metadata from that same file.
type MultiFilesOIDResolver struct {
	traps TrapSpec
}

// NewMultiFilesOIDResolver creates a new MultiFilesOIDResolver instance by loading json or yaml files
// (optionnally gzipped) located in the directory snmp.d/traps_db/
func NewMultiFilesOIDResolver() (*MultiFilesOIDResolver, error) {
	oidResolver := &MultiFilesOIDResolver{traps: make(TrapSpec)}
	confdPath := config.Datadog.GetString("confd_path")
	trapsDBRoot := filepath.Join(confdPath, "snmp.d", "traps_db")
	files, err := os.ReadDir(trapsDBRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir `%s`: %v", trapsDBRoot, err)
	}
	fileNames := getSortedFileNames(files)
	for _, fileName := range fileNames {
		err := oidResolver.updateFromFile(filepath.Join(trapsDBRoot, fileName))
		if err != nil {
			log.Warnf("unable to load trap db file %s: %s", fileName, err)
		}
	}
	return oidResolver, nil
}

// GetTrapMetadata returns TrapMetadata for a given trapOID
func (or *MultiFilesOIDResolver) GetTrapMetadata(trapOID string) (TrapMetadata, error) {
	trapOID = strings.TrimSuffix(NormalizeOID(trapOID), ".0")
	trapData, ok := or.traps[trapOID]
	if !ok {
		return TrapMetadata{}, fmt.Errorf("trap OID %s is not defined", trapOID)
	}
	return trapData, nil
}

// GetVariableMetadata returns VariableMetadata for a given variableOID and trapOID.
// The trapOID should not be needed in theory but the Datadog Agent allows to define multiple variable names for the
// same OID as long as they are defined in different file. The trapOID is used to differentiate between these files.
func (or *MultiFilesOIDResolver) GetVariableMetadata(trapOID string, varOID string) (VariableMetadata, error) {
	trapOID = strings.TrimSuffix(NormalizeOID(trapOID), ".0")
	varOID = strings.TrimSuffix(NormalizeOID(varOID), ".0")
	trapData, ok := or.traps[trapOID]
	if !ok {
		return VariableMetadata{}, fmt.Errorf("trap OID %s is not defined", trapOID)
	}
	varData, ok := trapData.variableSpecPtr[varOID]
	if !ok {
		return VariableMetadata{}, fmt.Errorf("variable OID %s is not defined", varOID)
	}
	return varData, nil
}

func getSortedFileNames(files []fs.DirEntry) []string {
	fileNames := make([]string, 0, len(files))
	for _, file := range files {
		if file.IsDir() {
			log.Debugf("not loading traps data from path %s: file is directory", file.Name())
			continue
		}
		fileNames = append(fileNames, file.Name())
	}

	// Sort files alphabetically but put first the one shipped with the agent
	sort.Slice(fileNames, func(i, j int) bool {
		fileNameI := strings.ToLower(fileNames[i])
		fileNameJ := strings.ToLower(fileNames[j])
		if strings.HasPrefix(fileNameI, ddTrapDBFileNamePrefix) {
			return true
		} else if strings.HasPrefix(fileNameJ, ddTrapDBFileNamePrefix) {
			return false
		}
		return fileNameI < fileNameJ
	})
	return fileNames
}

func (or *MultiFilesOIDResolver) updateFromFile(filePath string) error {
	var fileReader io.ReadCloser
	fileReader, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileReader.Close()
	if strings.HasSuffix(filePath, ".gz") {
		filePath = strings.TrimSuffix(filePath, ".gz")
		uncompressor, err := gzip.NewReader(fileReader)
		if err != nil {
			return fmt.Errorf("unable to uncompress gzip file %s", filePath)
		}
		defer uncompressor.Close()
		fileReader = uncompressor
	}
	var unmarshalMethod unmarshaller = yaml.Unmarshal
	if strings.HasSuffix(filePath, ".json") {
		unmarshalMethod = json.Unmarshal
	}
	return or.updateFromReader(fileReader, unmarshalMethod)
}

func (or *MultiFilesOIDResolver) updateFromReader(reader io.Reader, unmarshalMethod unmarshaller) error {
	fileContent, err := ioutil.ReadAll(reader)
	if err != nil {
		return err
	}
	var trapData trapDBFileContent
	err = unmarshalMethod(fileContent, &trapData)
	if err != nil {
		return err
	}

	return or.updateResolverWithData(trapData)
}

func (or *MultiFilesOIDResolver) updateResolverWithData(trapDB trapDBFileContent) error {
	definedVariables := variableSpec{}
	for variableOID, variableData := range trapDB.Variables {
		variableOID := NormalizeOID(variableOID)
		definedVariables[variableOID] = variableData
	}

	for trapOID, trapData := range trapDB.Traps {
		trapOID := NormalizeOID(trapOID)
		if _, trapConflict := or.traps[trapOID]; trapConflict {
			log.Debugf("a trap with OID %s is defined in multiple traps db files", trapOID)
		}
		or.traps[trapOID] = TrapMetadata{
			Name:            trapData.Name,
			Description:     trapData.Description,
			variableSpecPtr: definedVariables,
		}
	}
	return nil
}

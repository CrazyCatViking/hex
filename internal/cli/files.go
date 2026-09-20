package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func atomicWrite(path string, data []byte, mode fs.FileMode) (result error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hex-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() {
		if err := os.Remove(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeError := file.Write(data)
	permissionError := file.Chmod(mode)
	closeError := file.Close()
	if err := errors.Join(writeError, permissionError, closeError); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeError := io.Copy(file, bytes.NewReader(data))
	return errors.Join(writeError, file.Close())
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, statError := file.Stat()
	if statError != nil {
		return nil, errors.Join(statError, file.Close())
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.Join(fmt.Errorf("expected a regular file of at most %d bytes", limit), file.Close())
	}
	data, readError := io.ReadAll(io.LimitReader(file, limit+1))
	if err := errors.Join(readError, file.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

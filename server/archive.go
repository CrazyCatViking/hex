package hex

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"path"
)

type archiveError struct {
	status  int
	message string
}

func (e *archiveError) Error() string {
	return e.message
}

func writeDeploymentError(w http.ResponseWriter, err error) {
	var invalidArchive *archiveError
	if errors.As(err, &invalidArchive) {
		writeError(w, invalidArchive.status, invalidArchive.message)
		return
	}

	writeServerError(w, err)
}

func validateArchive(data []byte, maxBytes int64) (*zip.Reader, map[string]bool, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, &archiveError{http.StatusBadRequest, "expected ZIP archive"}
	}
	if len(archive.File) > 5000 {
		return nil, nil, &archiveError{http.StatusBadRequest, "maximum 5000 entries"}
	}

	files := make(map[string]bool)
	var totalSize uint64
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}

		if !validKey(file.Name) || !file.Mode().IsRegular() || files[file.Name] {
			return nil, nil, &archiveError{http.StatusBadRequest, "invalid or duplicate archive path"}
		}
		if file.UncompressedSize64 > uint64(maxBytes)-totalSize {
			return nil, nil, &archiveError{http.StatusRequestEntityTooLarge, "expanded deployment exceeds limit"}
		}

		totalSize += file.UncompressedSize64
		files[file.Name] = true
	}

	if !files["index.html"] {
		return nil, nil, &archiveError{http.StatusBadRequest, "index.html is required at archive root"}
	}

	for key := range files {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if files[parent] {
				return nil, nil, &archiveError{http.StatusBadRequest, "archive path is both a file and directory"}
			}
		}
	}

	return archive, files, nil
}

func readArchiveFile(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, &archiveError{http.StatusBadRequest, "invalid ZIP entry"}
	}
	defer closeReader(reader, file.Name)

	content, err := io.ReadAll(io.LimitReader(reader, int64(file.UncompressedSize64)+1))
	if err != nil || uint64(len(content)) != file.UncompressedSize64 {
		return nil, &archiveError{http.StatusBadRequest, "corrupt ZIP entry"}
	}

	return content, nil
}

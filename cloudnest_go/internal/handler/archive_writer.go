package handler

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"
)

type archiveOutput struct {
	ctx context.Context
	out io.Writer
}

func (w archiveOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.out.Write(p)
}

func releaseArchive(a *transfer.Handle) {
	if err := a.Close(true); err != nil {
		log.Print("archive cleanup retained for transfer reconciliation")
	}
}

func (h *FileHandler) buildArchive(ctx context.Context, uid int64, write func(context.Context, *zip.Writer) error) (result *transfer.Handle, resultErr error) {
	a, err := h.Transfers.CreateArchive(ctx, uid)
	if err != nil {
		return nil, err
	}
	build, stop := context.WithTimeout(a.Context(), 10*time.Minute)
	defer stop()
	zw := zip.NewWriter(archiveOutput{ctx: build, out: a})
	ok := false
	defer func() {
		if !ok {
			if cause := context.Cause(build); cause != nil {
				resultErr = cause
				if errors.Is(cause, context.DeadlineExceeded) && ctx.Err() == nil {
					resultErr = errors.Join(contract.ErrTransferUnavailable, cause)
				}
			}
			stop()
			_ = zw.Close()
			releaseArchive(a)
		}
	}()
	if err := write(build, zw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if err := a.SealArchive(); err != nil {
		return nil, err
	}
	ok = true
	return a, nil
}

func (h *FileHandler) writeArchiveFile(ctx context.Context, zw *zip.Writer, name string, storageID int64) error {
	object, err := h.Svc.GetStorageByID(ctx, storageID)
	if err != nil {
		return err
	}
	out, err := zw.Create(filepath.ToSlash(name))
	if err != nil {
		return err
	}
	_, err = blob.CopyPinnedStorage(ctx, out, object)
	return err
}
func (h *FileHandler) writeArchiveFolder(ctx context.Context, zw *zip.Writer, entries []service.FileFolderZipEntry) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir {
			if _, err := zw.Create(filepath.ToSlash(strings.TrimSuffix(entry.RelPath, "/")) + "/"); err != nil {
				return err
			}
		} else if err := h.writeArchiveFile(ctx, zw, entry.RelPath, entry.StorageID); err != nil {
			return err
		}
	}
	return nil
}
func (h *FileHandler) createFolderZip(ctx context.Context, uid int64, root model.File) (*transfer.Handle, error) {
	entries, err := h.Svc.CollectFolderZipEntries(ctx, uid, root.ID)
	if err != nil {
		return nil, err
	}
	return h.buildArchive(ctx, uid, func(ctx context.Context, zw *zip.Writer) error { return h.writeArchiveFolder(ctx, zw, entries) })
}
func (h *FileHandler) createBatchZip(ctx context.Context, uid int64, entries []service.FileBatchDownloadEntry) (*transfer.Handle, error) {
	return h.buildArchive(ctx, uid, func(ctx context.Context, zw *zip.Writer) error {
		used := map[string]bool{}
		for _, entry := range entries {
			name := entry.File.Filename
			if entry.IsFolderZip {
				name += ".zip"
			}
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			candidate := name
			for n := 1; used[candidate]; n++ {
				candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
			}
			used[candidate] = true
			if entry.IsFolderZip {
				out, err := zw.Create(candidate)
				if err != nil {
					return err
				}
				inner := zip.NewWriter(out)
				err = h.writeArchiveFolder(ctx, inner, entry.FolderEntries)
				closeErr := inner.Close()
				if err != nil || closeErr != nil {
					return errors.Join(err, closeErr)
				}
			} else {
				if entry.File.StorageID == nil {
					return service.ErrStorageNotFound
				}
				if err := h.writeArchiveFile(ctx, zw, candidate, *entry.File.StorageID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

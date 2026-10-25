package attachments

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

// AgentDir is where a task's attachment folder is mounted, read-only, in its
// agent container.
const AgentDir = "/attachments"

const thumbsDir = ".thumbs"

// Ext is the file extension for a stored attachment's MIME type.
func Ext(mime string) string {
	switch mime {
	case fmtPNG.mime:
		return fmtPNG.ext
	case fmtJPEG.mime:
		return fmtJPEG.ext
	case fmtGIF.mime:
		return fmtGIF.ext
	case fmtWebP.mime:
		return fmtWebP.ext
	}
	return "bin"
}

func fileName(a *store.Attachment) string { return strconv.FormatInt(a.ID, 10) + "." + Ext(a.Mime) }

// AgentPath is the attachment's path inside the agent container.
func AgentPath(a *store.Attachment) string { return AgentDir + "/" + fileName(a) }

// Files stores attachment files under Dir: <Dir>/<task_id>/<id>.<ext>, with
// thumbnails in <Dir>/<task_id>/.thumbs/<id>.jpg. Dir is the same path on the
// host and in the server container, so it also works as a bind mount source.
type Files struct {
	Dir string
	// mu orders uploads (read lock while rows and files are written) against
	// sweeps (write lock), so a sweep never removes the file of an upload
	// whose transaction has not committed yet.
	mu sync.RWMutex
}

func (f *Files) TaskDir(taskID int64) string {
	return filepath.Join(f.Dir, strconv.FormatInt(taskID, 10))
}

func (f *Files) Path(a *store.Attachment) string {
	return filepath.Join(f.TaskDir(a.TaskID), fileName(a))
}

func (f *Files) ThumbPath(a *store.Attachment) string {
	return filepath.Join(f.TaskDir(a.TaskID), thumbsDir, strconv.FormatInt(a.ID, 10)+".jpg")
}

// Hold blocks sweeps until the returned function is called. Wrap the store
// call that inserts attachment rows (and places their files) in it.
func (f *Files) Hold() func() {
	f.mu.RLock()
	return f.mu.RUnlock
}

// Place returns the store.Upload.Place function for imgs: it writes each
// image (and its thumbnail) under the id of its row.
func (f *Files) Place(imgs []*Image) func(taskID int64, rows []*store.Attachment) error {
	return func(taskID int64, rows []*store.Attachment) error {
		if len(rows) != len(imgs) {
			return fmt.Errorf("attachments: %d rows for %d images", len(rows), len(imgs))
		}
		if err := os.MkdirAll(filepath.Join(f.TaskDir(taskID), thumbsDir), 0o750); err != nil {
			return err
		}
		for i, a := range rows {
			if err := writeFile(f.Path(a), imgs[i].Data); err != nil {
				return err
			}
			if err := writeFile(f.ThumbPath(a), imgs[i].Thumb); err != nil {
				return err
			}
		}
		return nil
	}
}

// writeFile writes through a temporary name, so a reader never sees half a file.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Sweep removes a task's files that have no live attachment row (deleted
// attachments, leftovers of failed uploads) and the whole folder when
// nothing is live.
func (f *Files) Sweep(ctx context.Context, st *store.Store, taskID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sweep(ctx, st, taskID)
}

func (f *Files) sweep(ctx context.Context, st *store.Store, taskID int64) error {
	dir := f.TaskDir(taskID)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	live, err := st.LiveAttachments(ctx, taskID)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return os.RemoveAll(dir)
	}
	keep := map[string]bool{}
	for _, a := range live {
		keep[f.Path(a)] = true
		keep[f.ThumbPath(a)] = true
	}
	var errs []error
	for _, d := range []string{dir, filepath.Join(dir, thumbsDir)} {
		entries, err := os.ReadDir(d)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if e.IsDir() || keep[p] {
				continue
			}
			if err := os.Remove(p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Watch sweeps a task's folder whenever the task reaches a status that
// deletes attachments (completed, or failed/cancelled dropping a queued
// message's images), so files go right away rather than at the next
// maintenance pass. It returns when ctx ends.
func (f *Files) Watch(ctx context.Context, st *store.Store, hub *events.Hub) {
	ch, cancel := hub.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.Task == nil {
				continue
			}
			switch m.Task.Status {
			case "completed", "failed", "cancelled":
				if err := f.Sweep(ctx, st, m.Task.ID); err != nil {
					slog.Warn("attachments: sweep", "task_id", m.Task.ID, "err", err)
				}
			}
		}
	}
}

// SweepAll sweeps every task folder under Dir; folders of tasks that no
// longer exist or have no live attachments are removed. Entries that are
// not task folders are left alone.
func (f *Files) SweepAll(ctx context.Context, st *store.Store) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := os.ReadDir(f.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if !e.IsDir() || err != nil || id <= 0 {
			continue
		}
		if err := f.sweep(ctx, st, id); err != nil {
			errs = append(errs, fmt.Errorf("task %d: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Package export builds a logical backup of a LiteZap server (cloud spec E8): a tar.gz
// with a consistent copy of litezap.db (online backup, operator data removed), the
// downloaded media and a manifest. WhatsApp sessions, tokens and keys are never
// included: restoring elsewhere means pairing the numbers again.
package export

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// Format identifies the archive layout in manifest.json.
const (
	Format        = "litezap-export"
	FormatVersion = 1
)

// Manifest is manifest.json, the first entry of the archive.
type Manifest struct {
	Format        string          `json:"format"`
	FormatVersion int             `json:"format_version"`
	EngineVersion string          `json:"engine_version"`
	CreatedAt     time.Time       `json:"created_at"`
	IncludesMedia bool            `json:"includes_media"`
	Instances     []InstanceEntry `json:"instances"`
	// Excluded lists what is deliberately not in the archive.
	Excluded []string `json:"excluded"`
}

type InstanceEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Phone string `json:"phone,omitempty"`
}

// Exporter serves POST /v1/export (behind the admin authentication).
type Exporter struct {
	Store   *store.Store
	DataDir string
	Version string
	Log     *slog.Logger
	// Client uploads to upload_url (default: 30 min timeout).
	Client *http.Client

	mu sync.Mutex // one export at a time (disk space, I/O)
}

type request struct {
	IncludeMedia *bool  `json:"include_media"`
	UploadURL    string `json:"upload_url"`
}

func (e *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req request
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, apierr.Validation("invalid JSON body"))
			return
		}
	}
	includeMedia := req.IncludeMedia == nil || *req.IncludeMedia
	if req.UploadURL != "" {
		// The server makes the request: only the operator may point it somewhere.
		if sc, _ := service.ScopeFrom(r.Context()); !sc.Operator {
			apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "upload_url needs the operator token", false))
			return
		}
		if u, err := url.Parse(req.UploadURL); err != nil || u.Scheme != "https" || u.Host == "" {
			apierr.Write(w, apierr.Validation("upload_url must be an https URL (e.g. a presigned S3 PUT URL)"))
			return
		}
	}
	if !e.mu.TryLock() {
		apierr.Write(w, apierr.New(http.StatusConflict, "conflict", "an export is already running; try again later", true))
		return
	}
	defer e.mu.Unlock()
	ctx := context.WithoutCancel(r.Context())

	snap, cleanup, err := e.snapshot(ctx)
	if err != nil {
		e.logErr("export: snapshot", err)
		apierr.Write(w, apierr.Internal())
		return
	}
	defer cleanup()

	name := "litezap-export-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	if req.UploadURL == "" {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		if err := e.write(ctx, w, snap, includeMedia); err != nil {
			// Headers are gone: a truncated archive fails to decompress on the client.
			e.logErr("export: stream", err)
		}
		return
	}
	e.upload(ctx, w, req.UploadURL, snap, includeMedia, name)
}

func (e *Exporter) logErr(msg string, err error) {
	if e.Log != nil {
		e.Log.Error(msg, "err", err)
	}
}

// snapshot writes a scrubbed online backup of the database under DataDir/tmp.
func (e *Exporter) snapshot(ctx context.Context) (path string, cleanup func(), err error) {
	dir := filepath.Join(e.DataDir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	base := filepath.Join(dir, secure.NewID("export_"))
	path = base + ".db"
	cleanup = func() {
		for _, suffix := range []string{".db", ".db-wal", ".db-shm", ".db-journal", ".tar.gz"} {
			_ = os.Remove(base + suffix)
		}
	}
	if err := e.Store.BackupTo(ctx, path); err != nil {
		cleanup()
		return "", nil, err
	}
	cp, err := store.Open(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	err = cp.ScrubForExport(ctx)
	if cerr := cp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func (e *Exporter) manifest(ctx context.Context, includeMedia bool) (Manifest, error) {
	list, err := e.Store.ListInstances(ctx)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Format: Format, FormatVersion: FormatVersion, EngineVersion: e.Version, CreatedAt: time.Now().UTC(),
		IncludesMedia: includeMedia, Instances: []InstanceEntry{},
		Excluded: []string{"whatsapp_sessions", "admin_token", "operator_token", "instance_tokens", "media_key", "operator_webhooks"}}
	for _, in := range list {
		user, _, _ := strings.Cut(in.JID, "@")
		phone, _, _ := strings.Cut(user, ":")
		m.Instances = append(m.Instances, InstanceEntry{ID: in.ID, Name: in.Name, State: string(in.State), Phone: phone})
	}
	return m, nil
}

// write streams the archive: manifest.json, litezap.db, then media/<instance>/<file>.
func (e *Exporter) write(ctx context.Context, w io.Writer, dbPath string, includeMedia bool) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	m, err := e.manifest(ctx, includeMedia)
	if err != nil {
		return err
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	now := time.Now()
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(mb)), ModTime: now}); err != nil {
		return err
	}
	if _, err := tw.Write(mb); err != nil {
		return err
	}
	if err := addFile(tw, dbPath, "litezap.db"); err != nil {
		return err
	}
	if includeMedia {
		root := filepath.Join(e.DataDir, "media")
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() || strings.HasSuffix(d.Name(), ".part") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			if err := addFile(tw, p, "media/"+filepath.ToSlash(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err // a file removed meanwhile (retention) is skipped
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, st.Size())
	return err
}

// upload builds the archive in a temporary file, then PUTs it to the presigned URL.
func (e *Exporter) upload(ctx context.Context, w http.ResponseWriter, target, dbPath string, includeMedia bool, name string) {
	tmp := strings.TrimSuffix(dbPath, ".db") + ".tar.gz"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		e.logErr("export: temp file", err)
		apierr.Write(w, apierr.Internal())
		return
	}
	defer f.Close()
	h := sha256.New()
	if err := e.write(ctx, io.MultiWriter(f, h), dbPath, includeMedia); err != nil {
		e.logErr("export: build archive", err)
		apierr.Write(w, apierr.Internal())
		return
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		apierr.Write(w, apierr.Internal())
		return
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, io.NopCloser(f))
	if err != nil {
		apierr.Write(w, apierr.Validation("invalid upload_url"))
		return
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := client.Do(req)
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadGateway, "upload_failed", "uploading the export failed: "+redactURL(err.Error(), target), true))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		apierr.Write(w, apierr.New(http.StatusBadGateway, "upload_failed", fmt.Sprintf("upload_url answered HTTP %d", resp.StatusCode), true))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"uploaded": true, "name": name, "bytes": size,
		"sha256": hex.EncodeToString(h.Sum(nil))})
}

// redactURL keeps a presigned URL's signature out of error messages.
func redactURL(msg, target string) string {
	if u, err := url.Parse(target); err == nil {
		msg = strings.ReplaceAll(msg, target, u.Scheme+"://"+u.Host+"/…")
	}
	return msg
}

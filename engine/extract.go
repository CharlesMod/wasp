package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extract unpacks a .tar.gz or .zip into dir. An entry, or a symlink, that
// would lead out of dir is refused by name; layouts are not flattened.
func Extract(archive, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dir)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTarGz(archive, dir)
	}
	return fmt.Errorf("unknown archive type: %s", filepath.Base(archive))
}

// inside resolves name under dir, refusing a path that escapes it.
func inside(dir, name string) (string, error) {
	out := filepath.Join(dir, filepath.FromSlash(name))
	if out != filepath.Clean(dir) && !strings.HasPrefix(out, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path escapes the destination: %s", name)
	}
	return out, nil
}

func writeFile(out string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		out, err := inside(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(out, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archive, dir string) error {
	fh, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		out, err := inside(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(out, 0o755)
		case tar.TypeReg:
			err = writeFile(out, tr, os.FileMode(h.Mode))
		case tar.TypeSymlink:
			// The link's target, resolved from the link's own directory, must stay inside dir.
			target := h.Linkname
			resolved := target
			if !filepath.IsAbs(target) {
				resolved = filepath.Join(filepath.Dir(out), target)
			}
			if _, e := inside(dir, mustRel(dir, resolved)); e != nil || filepath.IsAbs(target) {
				return fmt.Errorf("archive symlink leaves the destination: %s -> %s", h.Name, h.Linkname)
			}
			if err = os.MkdirAll(filepath.Dir(out), 0o755); err == nil {
				_ = os.Remove(out)
				err = os.Symlink(target, out)
			}
		case tar.TypeLink:
			src, e := inside(dir, h.Linkname)
			if e != nil {
				return e
			}
			in, e := os.Open(src)
			if e != nil {
				return e
			}
			err = writeFile(out, in, os.FileMode(h.Mode))
			in.Close()
		}
		if err != nil {
			return err
		}
	}
}

package modules

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/bobg/errors"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// Each calls f for each Go module in dir and its subdirectories.
// A Go module is identified by the presence of a go.mod file.
// The argument to f is the directory containing the go.mod file,
// which will have dir as a prefix.
// This function calls Walker.Each with a default Walker.
func Each(dir string, f func(string) error) error {
	var w Walker
	return w.Each(dir, f)
}

// Walker is a controller for various methods that walk a directory tree of Go modules.
// The zero value is a valid walker.
type Walker struct {
	IncludeVendor   bool // If true, walk into vendor directories. If false, skip them.
	IncludeTestdata bool // If true, walk into testdata directories. If false, skip them.

	// The following fields are used by EachGomod and LoadEachGomod.

	ParseLax     bool                 // Use [modfile.ParseLax] to parse go.mod files instead of [modfile.Parse].
	VersionFixer modfile.VersionFixer // Use this version-string fixing function when parsing go.mod files.

	// The following fields are used by LoadEach and LoadEachGomod.

	// This is the config to pass to [packages.Load]
	// when loading packages in [Walker.LoadEach] and [Walker.LoadEachGomod].
	// If this is the zero config,
	// a default value of [DefaultLoadConfig] is used.
	// If this is not the zero config but LoadConfig.Mode is zero,
	// a default value of [LoadMode] is used.
	// The Dir field of the config is set to the directory passed to [Walker.LoadEach] or [Walker.LoadEachGomod].
	LoadConfig          packages.Config
	FailOnPackageErrors bool // If true, return an error if any package fails to load.
}

var zeroLoadConfig packages.Config

const LoadMode = packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedModule | packages.NeedEmbedFiles | packages.NeedEmbedPatterns

var DefaultLoadConfig = packages.Config{Mode: LoadMode}

// Each calls f for each Go module in dir and its subdirectories.
// A Go module is identified by the presence of a go.mod file.
// The arguments to f is the directory containing the go.mod file,
// which will have dir as a prefix.
func (w *Walker) Each(dir string, f func(string) error) error {
	err := w.each(dir, f)
	if errors.Is(err, filepath.SkipAll) {
		return nil
	}
	return err
}

func (w *Walker) each(dir string, f func(string) error) error {
	gomodPath := filepath.Join(dir, "go.mod")
	_, err := os.Stat(gomodPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// no go.mod, skip
	case err != nil:
		return errors.Wrapf(err, "statting %s", gomodPath)
	default:
		err := f(dir)
		switch {
		case errors.Is(err, filepath.SkipDir):
			return nil
		case err != nil: // including filepath.SkipAll, which gets filtered out in Walker.Each.
			return errors.Wrapf(err, "in %s", dir)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.Wrapf(err, "reading directory %s", dir)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if !w.IncludeVendor && name == "vendor" { // TODO: also check for vendor/modules.txt?
			continue
		}
		if !w.IncludeTestdata && name == "testdata" {
			continue
		}
		if err := w.each(filepath.Join(dir, entry.Name()), f); err != nil {
			return err
		}
	}

	return nil
}

// EachGomod calls f for each Go module in dir and its subdirectories.
// A Go module is identified by the presence of a go.mod file.
// The arguments to f are the directory containing the go.mod file
// (which will have dir as a prefix)
// and the parsed go.mod file.
// This function calls Walker.EachGomod with a default Walker.
func EachGomod(dir string, f func(string, *modfile.File) error) error {
	var w Walker
	return w.EachGomod(dir, f)
}

// EachGomod calls f for each Go module in dir and its subdirectories.
// A Go module is identified by the presence of a go.mod file.
// The arguments to f are the directory containing the go.mod file
// (which will have dir as a prefix)
// and the parsed go.mod file.
func (w *Walker) EachGomod(dir string, f func(string, *modfile.File) error) error {
	return w.Each(dir, func(subdir string) error {
		return w.withGomod(dir, subdir, f)
	})
}

func (w *Walker) withGomod(dir, subdir string, f func(string, *modfile.File) error) error {
	gomodPath := filepath.Join(subdir, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return errors.Wrapf(err, "reading %s", gomodPath)
	}

	var mf *modfile.File
	if w.ParseLax {
		mf, err = modfile.ParseLax(gomodPath, data, w.VersionFixer)
	} else {
		mf, err = modfile.Parse(gomodPath, data, w.VersionFixer)
	}
	if err != nil {
		return errors.Wrapf(err, "parsing %s", gomodPath)
	}

	return f(subdir, mf)
}

func LoadEach(dir string, f func(string, []*packages.Package) error) error {
	var w Walker
	return w.LoadEach(dir, f)
}

func (w *Walker) LoadEach(dir string, f func(string, []*packages.Package) error) error {
	conf := w.LoadConfig
	if isZeroConfig(conf) {
		conf = DefaultLoadConfig
	}
	if conf.Mode == 0 {
		conf.Mode = LoadMode
	}
	conf.Dir = dir

	return w.Each(dir, func(subdir string) error {
		pkgs, err := packages.Load(&conf, "./...")
		if err != nil {
			return errors.Wrapf(err, "loading packages in %s", subdir)
		}

		if w.FailOnPackageErrors {
			var err error
			for _, pkg := range pkgs {
				for _, pkgErr := range pkg.Errors {
					err = errors.Join(err, PackageLoadError{PkgPath: pkg.PkgPath, Err: pkgErr})
				}
			}
			if err != nil {
				return err
			}
		}

		return f(subdir, pkgs)
	})
}

func isZeroConfig(conf packages.Config) bool {
	return reflect.DeepEqual(conf, zeroLoadConfig) // Can't use == because packages.Config contains function pointers.
}

type PackageLoadError struct {
	PkgPath string
	Err     error
}

func (e PackageLoadError) Error() string {
	return fmt.Sprintf("loading %s: %s", e.PkgPath, e.Err)
}

func (e PackageLoadError) Unwrap() error {
	return e.Err
}

// LoadEachGomod combines LoadEach and EachGomod.
func LoadEachGomod(dir string, f func(string, *modfile.File, []*packages.Package) error) error {
	var w Walker
	return w.LoadEachGomod(dir, f)
}

// LoadEachGomod combines LoadEach and EachGomod.
func (w *Walker) LoadEachGomod(dir string, f func(string, *modfile.File, []*packages.Package) error) error {
	return w.LoadEach(dir, func(subdir string, pkgs []*packages.Package) error {
		return w.withGomod(dir, subdir, func(subdir string, mf *modfile.File) error {
			return f(subdir, mf, pkgs)
		})
	})
}

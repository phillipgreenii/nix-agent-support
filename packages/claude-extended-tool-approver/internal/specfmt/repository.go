package specfmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
)

// Layer names one of the three precedence layers a Repository reads,
// LOWEST precedence first — a later layer's spec for the same (Kind, Name)
// wins over an earlier one's (see MergedSet/Conflict).
type Layer int

const (
	// LayerEmbedded is the built-in layer compiled into the binary (an
	// embed.FS a caller supplies to NewRepository) — no filesystem
	// dependency at all.
	LayerEmbedded Layer = iota
	// LayerUser is the user-level layer, ordinarily DefaultUserDir().
	LayerUser
	// LayerRepo is the repo-level layer, ordinarily DefaultRepoDir(root).
	LayerRepo
)

// String returns the deterministic layer name used in Conflict messages.
func (l Layer) String() string {
	switch l {
	case LayerEmbedded:
		return "embedded"
	case LayerUser:
		return "user"
	case LayerRepo:
		return "repo"
	default:
		return "layer-invalid"
	}
}

// DefaultUserDir returns the user-level spec directory, resolved from the
// OS home directory (os.UserHomeDir) — deliberately NEVER $XDG_CONFIG_HOME,
// per P7's rule that a security-relevant config location is not read from
// the inherited environment (see this package's doc comment).
func DefaultUserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("specfmt: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "claude-extended-tool-approver"), nil
}

// DefaultRepoDir returns the repo-level spec directory for the repo rooted
// at repoRoot.
func DefaultRepoDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".ceta")
}

// key identifies a spec by its (Kind, Name) pair — the merge key the
// Repository keys ALL layers by, regardless of kind, so the merge machinery
// stays spec-kind-agnostic (see doc.go).
type key struct {
	Kind SpecKind
	Name string
}

// loaded is one spec as read from one layer, kept until merge time so a
// Conflict can name its layer and file path.
type loaded struct {
	layer Layer
	path  string
	spec  Spec
}

// Conflict records a later layer replacing an earlier layer's spec of the
// same (Kind, Name) WITHOUT that later spec declaring Overrides. The
// Repository itself does not fail on this — packet 1.3's linter is the
// enforcement point — it only surfaces the information a linter (or any
// other caller) needs to act on it.
type Conflict struct {
	Kind         SpecKind
	Name         string
	LosingLayer  Layer
	LosingPath   string
	WinningLayer Layer
	WinningPath  string
}

// MergedSet is the result of loading and merging all three layers.
// Commands holds the winning CommandSpecV1 for every KindCommand name seen
// (already validated and precedence-resolved); Conflicts lists every
// override that was not declared (see Conflict); Invalid lists every spec
// file that Validate rejected — those specs are EXCLUDED from Commands
// entirely (fail-closed, not silently merged) rather than aborting the
// whole Load.
type MergedSet struct {
	Commands  map[string]CommandSpecV1
	Conflicts []Conflict
	Invalid   []InvalidSpec
}

// InvalidSpec names one spec file that failed Validate, and why.
type InvalidSpec struct {
	Layer Layer
	Path  string
	Err   error
}

// Repository loads specs from the three fs.FS layers and merges them. All
// three fields may be nil/empty — a nil embedded FS or a directory that does
// not exist is treated as an empty layer, not an error (see loadLayer).
type Repository struct {
	Embedded fs.FS
	UserDir  string
	RepoDir  string
}

// NewRepository builds a Repository over the three layers. embedded is
// typically an embed.FS a later packet compiles in (nil is a valid empty
// embedded layer, e.g. in this packet's own tests); userDir and repoDir are
// plain directory paths (DefaultUserDir/DefaultRepoDir supply the
// conventional ones) — a directory that does not exist is simply an empty
// layer, not an error, so a fresh checkout with no ~/.config/
// claude-extended-tool-approver/ or .ceta/ yet works unchanged.
func NewRepository(embedded fs.FS, userDir, repoDir string) *Repository {
	return &Repository{Embedded: embedded, UserDir: userDir, RepoDir: repoDir}
}

// Load reads all three layers, validates every spec found (Validate),
// excludes any that fail validation (recorded in MergedSet.Invalid), and
// merges the rest in precedence order embedded < user < repo, recording an
// undeclared override as a Conflict. The returned error is non-nil only for
// an I/O or JSON-decode failure that is NOT a per-spec validation problem
// (a directory that cannot be read once it is known to exist, malformed
// JSON) — those abort Load entirely, since there is no partial spec to
// exclude and keep going with.
func (r *Repository) Load() (*MergedSet, error) {
	var all []loaded

	embeddedFS := r.Embedded
	if embeddedFS != nil {
		got, err := loadLayer(embeddedFS, LayerEmbedded, ".")
		if err != nil {
			return nil, fmt.Errorf("specfmt: loading embedded layer: %w", err)
		}
		all = append(all, got...)
	}

	for dir, layer := range map[string]Layer{r.UserDir: LayerUser, r.RepoDir: LayerRepo} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("specfmt: statting %s layer directory %s: %w", layer, dir, err)
		}
		got, err := loadLayer(os.DirFS(dir), layer, ".")
		if err != nil {
			return nil, fmt.Errorf("specfmt: loading %s layer from %s: %w", layer, dir, err)
		}
		all = append(all, got...)
	}

	// Deterministic processing order: by layer (embedded, user, repo, i.e.
	// precedence order), then by path within a layer.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].layer != all[j].layer {
			return all[i].layer < all[j].layer
		}
		return all[i].path < all[j].path
	})

	merged := &MergedSet{Commands: map[string]CommandSpecV1{}}
	winners := map[key]loaded{}

	for _, l := range all {
		if err := Validate(l.spec); err != nil {
			merged.Invalid = append(merged.Invalid, InvalidSpec{Layer: l.layer, Path: l.path, Err: err})
			continue
		}
		k := key{Kind: l.spec.Kind, Name: l.spec.Name}
		if prev, ok := winners[k]; ok {
			if !l.spec.Overrides {
				merged.Conflicts = append(merged.Conflicts, Conflict{
					Kind:         k.Kind,
					Name:         k.Name,
					LosingLayer:  prev.layer,
					LosingPath:   prev.path,
					WinningLayer: l.layer,
					WinningPath:  l.path,
				})
			}
		}
		winners[k] = l
	}

	for k, l := range winners {
		if k.Kind != KindCommand {
			continue
		}
		merged.Commands[k.Name] = *l.spec.Command
	}

	return merged, nil
}

// loadLayer walks every "*.json" file under root in fsys and decodes it as
// a Spec. It is the ONE place any layer's files are read, so embedded,
// user-level and repo-level directories are all subject to identical
// parsing regardless of which fs.FS backs them.
func loadLayer(fsys fs.FS, layer Layer, root string) ([]loaded, error) {
	var out []loaded
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || stdpath.Ext(path) != ".json" {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		var spec Spec
		if err := json.Unmarshal(data, &spec); err != nil {
			return fmt.Errorf("decoding %s: %w", path, err)
		}
		out = append(out, loaded{layer: layer, path: path, spec: spec})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

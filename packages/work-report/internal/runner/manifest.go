//go:build unix

package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"
)

// Rule names of manifest validation. They appear in the failure reason
// (together with the offending path) and are recorded in the ADR.
const (
	ruleJSON            = "manifest_json"
	ruleContractVersion = "contract_version"
	ruleFiles           = "files_non_empty"
	ruleFilesMax        = "files_max"
	ruleFileItem        = "file_item"
	rulePathRelative    = "path_relative"
	rulePathClean       = "path_clean"
	rulePathDotDot      = "path_no_dotdot"
	rulePathNUL         = "path_no_nul"
	rulePathBackslash   = "path_no_backslash"
	rulePathTrailing    = "path_no_trailing_slash"
	rulePathReserved    = "path_reserved"
	rulePathUnique      = "path_unique"
	ruleResolved        = "path_resolves"
	ruleInside          = "inside_output_dir"
	ruleResolvedRes     = "resolved_path_reserved"
	ruleResolvedUniq    = "resolved_path_unique"
	ruleRegular         = "regular_file"
	ruleLinkCount       = "link_count"
	ruleNonEmpty        = "non_empty"
	ruleContentType     = "content_type"
	ruleFileSize        = "file_size"
	ruleTotalSize       = "total_size"
	ruleSummary         = "summary"
	ruleWarnings        = "warnings"
)

// failure is a classified run failure.
type failure struct {
	kind   string
	reason string
}

func (f *failure) Error() string { return f.kind + ": " + f.reason }

func ruleFailure(rule, p, detail string) *failure {
	if p == "" {
		return &failure{KindInvalidManifest, fmt.Sprintf("manifest rule %q violated: %s", rule, detail)}
	}
	return &failure{KindInvalidManifest, fmt.Sprintf("manifest rule %q violated by path %q: %s", rule, p, detail)}
}

// fold is the comparison key of the case- and normalization-insensitive path
// rules (the default macOS volume is both).
func fold(p string) string { return strings.ToLower(norm.NFC.String(p)) }

func isReserved(p string) bool {
	f := fold(p)
	return f == ManifestName || f == StderrName
}

// validated is a manifest that passed every rule, with its files hashed.
type validated struct {
	files    []RecordFile
	summary  string
	warnings []string
}

// validateManifest checks stdout against the manifest rules for the files in
// runDir and hashes the listed files. It returns a *failure naming the rule
// and the offending path on a violation.
func validateManifest(stdout []byte, runDir string) (*validated, *failure) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.UseNumber()
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return nil, ruleFailure(ruleJSON, "", "stdout is not a JSON object: "+err.Error())
	}
	if top == nil {
		return nil, ruleFailure(ruleJSON, "", "stdout is not a JSON object")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ruleFailure(ruleJSON, "", "stdout holds more than one JSON value")
	}

	// contract_version first: a different version's other fields are not ours
	// to judge.
	rawCV, ok := top["contract_version"]
	if !ok || string(bytes.TrimSpace(rawCV)) == "null" {
		return nil, ruleFailure(ruleContractVersion, "", "contract_version is missing")
	}
	cv, err := strconv.Atoi(strings.TrimSpace(string(rawCV)))
	if err != nil {
		return nil, ruleFailure(ruleContractVersion, "", "contract_version must be an integer, got "+string(rawCV))
	}
	supported := false
	for _, v := range SupportedContractVersions {
		supported = supported || v == cv
	}
	if !supported {
		return nil, &failure{KindContractVersion, fmt.Sprintf("generator declares contract_version %d; work-report supports contract_version %s", cv, joinInts(SupportedContractVersions))}
	}

	v := &validated{}
	if raw, ok := top["summary"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &v.summary); err != nil {
			return nil, ruleFailure(ruleSummary, "", "summary must be a string")
		}
	}
	if raw, ok := top["warnings"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &v.warnings); err != nil {
			return nil, ruleFailure(ruleWarnings, "", "warnings must be an array of strings")
		}
	}

	rawFiles, ok := top["files"]
	trimmed := string(bytes.TrimSpace(rawFiles))
	if !ok || trimmed == "null" {
		return nil, ruleFailure(ruleFiles, "", "files must be a non-empty array, got "+nullOrMissing(ok))
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawFiles, &items); err != nil {
		return nil, ruleFailure(ruleFiles, "", "files must be an array")
	}
	if len(items) == 0 {
		return nil, ruleFailure(ruleFiles, "", "files must be a non-empty array")
	}
	if len(items) > MaxFiles {
		return nil, ruleFailure(ruleFilesMax, "", fmt.Sprintf("%d files listed, at most %d are allowed", len(items), MaxFiles))
	}

	dirResolved, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, &failure{KindOutputDirUnwritable, "cannot resolve the run directory: " + err.Error()}
	}

	listed := make(map[string]string, len(items))
	resolvedSeen := make(map[string]string, len(items))
	var total int64
	var resolvedPaths []string
	for i, raw := range items {
		var it struct {
			Path        *json.RawMessage `json:"path"`
			ContentType *json.RawMessage `json:"content_type"`
			Title       *json.RawMessage `json:"title"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, ruleFailure(ruleFileItem, "", fmt.Sprintf("files[%d] must be an object", i))
		}
		var p, ct, title string
		if it.Path == nil || json.Unmarshal(*it.Path, &p) != nil {
			return nil, ruleFailure(ruleFileItem, "", fmt.Sprintf("files[%d].path must be a string", i))
		}
		if it.ContentType == nil || json.Unmarshal(*it.ContentType, &ct) != nil || ct == "" {
			return nil, ruleFailure(ruleContentType, p, "content_type must be a non-empty string")
		}
		if it.Title != nil && string(bytes.TrimSpace(*it.Title)) != "null" && json.Unmarshal(*it.Title, &title) != nil {
			return nil, ruleFailure(ruleFileItem, p, "title must be a string")
		}
		if f := checkPath(p); f != nil {
			return nil, f
		}
		if mt, _, err := mime.ParseMediaType(ct); err != nil || strings.Count(mt, "/") != 1 {
			return nil, ruleFailure(ruleContentType, p, fmt.Sprintf("content_type %q is not a media type", ct))
		}
		key := fold(p)
		if first, dup := listed[key]; dup {
			return nil, ruleFailure(rulePathUnique, p, fmt.Sprintf("duplicates %q under case-folding and NFC normalization", first))
		}
		listed[key] = p

		resolved, err := filepath.EvalSymlinks(filepath.Join(runDir, filepath.FromSlash(p)))
		if err != nil {
			return nil, ruleFailure(ruleResolved, p, "file cannot be resolved: "+err.Error())
		}
		rel, err := filepath.Rel(dirResolved, resolved)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, ruleFailure(ruleInside, p, fmt.Sprintf("resolves to %q, outside the output directory", resolved))
		}
		rel = filepath.ToSlash(rel)
		if isReserved(rel) {
			return nil, ruleFailure(ruleResolvedRes, p, fmt.Sprintf("resolves to the reserved name %q", rel))
		}
		rkey := fold(rel)
		if first, dup := resolvedSeen[rkey]; dup {
			return nil, ruleFailure(ruleResolvedUniq, p, fmt.Sprintf("resolves to %q, the same file as %q", rel, first))
		}
		resolvedSeen[rkey] = p

		fi, err := os.Lstat(resolved)
		if err != nil {
			return nil, ruleFailure(ruleResolved, p, "file cannot be inspected: "+err.Error())
		}
		if !fi.Mode().IsRegular() {
			return nil, ruleFailure(ruleRegular, p, "not a regular file ("+fi.Mode().Type().String()+")")
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && uint64(st.Nlink) != 1 {
			return nil, ruleFailure(ruleLinkCount, p, fmt.Sprintf("link count is %d, must be 1", uint64(st.Nlink)))
		}
		if fi.Size() == 0 {
			return nil, ruleFailure(ruleNonEmpty, p, "file is empty")
		}
		if fi.Size() > MaxFileSize {
			return nil, ruleFailure(ruleFileSize, p, fmt.Sprintf("%d bytes, at most %d are allowed per file", fi.Size(), int64(MaxFileSize)))
		}
		total += fi.Size()
		if total > MaxTotalSize {
			return nil, ruleFailure(ruleTotalSize, p, fmt.Sprintf("listed files exceed %d bytes in total", int64(MaxTotalSize)))
		}
		v.files = append(v.files, RecordFile{Path: p, ContentType: ct, Title: title, SizeBytes: fi.Size()})
		resolvedPaths = append(resolvedPaths, resolved)
	}

	for i := range v.files {
		sum, n, err := hashFile(resolvedPaths[i])
		if err != nil {
			return nil, ruleFailure(ruleResolved, v.files[i].Path, "file cannot be read: "+err.Error())
		}
		v.files[i].SHA256 = sum
		v.files[i].SizeBytes = n
	}
	return v, nil
}

func nullOrMissing(present bool) string {
	if present {
		return "null"
	}
	return "nothing (the key is missing)"
}

func joinInts(vs []int) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

// checkPath applies the lexical path rules.
func checkPath(p string) *failure {
	switch {
	case p == "":
		return ruleFailure(rulePathRelative, p, "path is empty")
	case strings.ContainsRune(p, 0):
		return ruleFailure(rulePathNUL, p, "path contains a NUL byte")
	case strings.Contains(p, `\`):
		return ruleFailure(rulePathBackslash, p, "path contains a backslash")
	case strings.HasPrefix(p, "/"):
		return ruleFailure(rulePathRelative, p, "path is absolute")
	case strings.HasSuffix(p, "/"):
		return ruleFailure(rulePathTrailing, p, "path ends in a slash")
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return ruleFailure(rulePathDotDot, p, "path contains a .. element")
		}
	}
	if path.Clean(p) != p {
		return ruleFailure(rulePathClean, p, fmt.Sprintf("path is not clean (%q)", path.Clean(p)))
	}
	if isReserved(p) {
		return ruleFailure(rulePathReserved, p, "path names a file work-report owns")
	}
	return nil
}

func hashFile(p string) (sum string, n int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

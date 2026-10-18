// Package artifact reads plugin declarations without loading a plugin runtime.
package artifact

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const MaxDownload = 64 << 20
const MaxModule = 128 << 20
const MaxMetadata = 1 << 20

var identifier = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,128}$`)
var checksum = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Declaration struct {
	Kind          string          `json:"kind"`
	SHA256        string          `json:"sha256"`
	PluginID      string          `json:"plugin_id"`
	PluginVersion string          `json:"plugin_version"`
	Repository    string          `json:"repository"`
	Contract      string          `json:"contract"`
	Metadata      json.RawMessage `json:"metadata"`
}

type metadata struct {
	Version       int    `json:"metadata_version"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	License       string `json:"license"`
	Repository    string `json:"repository"`
	PluginVersion string `json:"plugin_version"`
	Transport     string `json:"transport"`
}

func text(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func declaration(kind string, data []byte) (Declaration, error) {
	var m metadata
	if len(data) == 0 || len(data) > MaxMetadata {
		return Declaration{}, errors.New("plugin metadata is missing or too large")
	}
	if err := jsonv2.Unmarshal(data, &m); err != nil {
		return Declaration{}, fmt.Errorf("plugin metadata: %w", err)
	}
	if m.Version != 1 || !identifier.MatchString(m.ID) || !text(m.Name, 256) || !text(m.Description, 4096) || !text(m.License, 256) || !text(m.PluginVersion, 128) {
		return Declaration{}, errors.New("invalid plugin declaration")
	}
	u, err := url.Parse(m.Repository)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return Declaration{}, errors.New("plugin repository must be an absolute HTTPS repository URL")
	}
	var contract string
	switch kind {
	case "core":
		var core struct {
			ABI uint32 `json:"abi_version"`
		}
		if err := jsonv2.Unmarshal(data, &core); err != nil {
			return Declaration{}, err
		}
		if m.Transport != "wasm-core-json" || core.ABI == 0 {
			return Declaration{}, errors.New("core declaration requires a transport and a positive ABI version")
		}
		contract = m.Transport + "/" + strconv.FormatUint(uint64(core.ABI), 10)
	case "component":
		var component struct {
			Interface string `json:"interface"`
			Digest    string `json:"contract_sha256"`
		}
		if err := jsonv2.Unmarshal(data, &component); err != nil {
			return Declaration{}, err
		}
		if m.Transport != "wasm-component" || !text(component.Interface, 512) || !checksum.MatchString(component.Digest) {
			return Declaration{}, errors.New("component declaration requires an interface and contract checksum")
		}
		contract = component.Interface + "#sha256=" + component.Digest
	default:
		return Declaration{}, errors.New("unsupported artifact format")
	}
	return Declaration{Kind: kind, PluginID: m.ID, PluginVersion: m.PluginVersion, Repository: m.Repository, Contract: contract, Metadata: bytes.Clone(data)}, nil
}

func (d Declaration) Validate() error {
	parsed, err := declaration(d.Kind, d.Metadata)
	if err != nil {
		return err
	}
	if !checksum.MatchString(d.SHA256) || parsed.PluginID != d.PluginID || parsed.PluginVersion != d.PluginVersion || parsed.Repository != d.Repository || parsed.Contract != d.Contract {
		return errors.New("artifact declaration disagrees with its metadata or checksum")
	}
	return nil
}

func u32(data []byte, offset *int) (uint32, error) {
	var value uint32
	for i := range 5 {
		if *offset >= len(data) {
			return 0, io.ErrUnexpectedEOF
		}
		b := data[*offset]
		*offset += 1
		if i == 4 && b > 15 {
			return 0, errors.New("invalid WASM section length")
		}
		value |= uint32(b&127) << (7 * i)
		if b&128 == 0 {
			return value, nil
		}
	}
	return 0, errors.New("invalid WASM section length")
}

// Inspect validates the container framing and discovery metadata, not instructions or ABI semantics.
func Inspect(ctx context.Context, path string) (result Declaration, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	stat, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > MaxDownload {
		return result, errors.New("artifact must be a regular file of at most 64 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, MaxDownload+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return result, err
	}
	if len(raw) > MaxDownload {
		return result, errors.New("artifact exceeds 64 MiB")
	}
	sum := sha256.Sum256(raw)
	data := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		z, openErr := gzip.NewReader(bytes.NewReader(raw))
		if openErr != nil {
			return result, openErr
		}
		data, readErr = io.ReadAll(io.LimitReader(z, MaxModule+1))
		if err = errors.Join(readErr, z.Close()); err != nil {
			return result, err
		}
	}
	if len(data) > MaxModule {
		return result, errors.New("expanded artifact exceeds 128 MiB")
	}
	if len(data) < 8 || !bytes.Equal(data[:4], []byte{0, 97, 115, 109}) {
		return result, errors.New("invalid WASM header")
	}
	var kind string
	switch {
	case bytes.Equal(data[4:8], []byte{1, 0, 0, 0}):
		kind = "core"
	case bytes.Equal(data[4:8], []byte{13, 0, 1, 0}):
		kind = "component"
	default:
		return result, errors.New("unsupported WASM container version")
	}
	var embedded []byte
	for offset := 8; offset < len(data); {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		id := data[offset]
		offset++
		size, sectionErr := u32(data, &offset)
		if sectionErr != nil {
			return result, sectionErr
		}
		if uint64(size) > uint64(len(data)-offset) {
			return result, io.ErrUnexpectedEOF
		}
		section := data[offset : offset+int(size)]
		offset += int(size)
		if id != 0 {
			continue
		}
		nameOffset := 0
		nameSize, nameErr := u32(section, &nameOffset)
		if nameErr != nil {
			return result, nameErr
		}
		if uint64(nameSize) > uint64(len(section)-nameOffset) {
			return result, io.ErrUnexpectedEOF
		}
		name := section[nameOffset : nameOffset+int(nameSize)]
		if !utf8.Valid(name) {
			return result, errors.New("invalid WASM custom section name")
		}
		if string(name) != "actionlint.plugin" {
			continue
		}
		if embedded != nil {
			return result, errors.New("duplicate actionlint.plugin custom section")
		}
		embedded = section[nameOffset+int(nameSize):]
		if len(embedded) == 0 {
			return result, errors.New("empty actionlint.plugin custom section")
		}
	}
	result, err = declaration(kind, embedded)
	if err != nil {
		return result, err
	}
	result.SHA256 = hex.EncodeToString(sum[:])
	return result, nil
}

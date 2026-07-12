package minecraft

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/link-fgfgui/mod-downloader-core/global"
	"github.com/link-fgfgui/mod-downloader-core/logging"
	structs "github.com/link-fgfgui/mod-downloader-core/structs/minecraft"

	"github.com/BurntSushi/toml"
)

type ModMetadataParser interface {
	Name() string
	CanParse(r *zip.Reader) bool
	Parse(r *zip.Reader) (parsedModMetadata, error)
}

type parsedModMetadata struct {
	Mods       []structs.ModInfo
	NestedJars []string
}

// --- Fabric ---

type fabricModParser struct{}

func (fabricModParser) Name() string { return "fabric" }

func (fabricModParser) CanParse(r *zip.Reader) bool {
	return zipHasFile(r, "fabric.mod.json")
}

func (fabricModParser) Parse(r *zip.Reader) (parsedModMetadata, error) {
	data, err := readZipFile(r, "fabric.mod.json")
	if err != nil {
		return parsedModMetadata{}, err
	}

	var meta struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Version     string         `json:"version"`
		Description string         `json:"description"`
		Depends     map[string]any `json:"depends"`
		Jars        []struct {
			File string `json:"file"`
		} `json:"jars"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return parsedModMetadata{}, fmt.Errorf("parse fabric.mod.json: %w", err)
	}

	result := parsedModMetadata{
		NestedJars: make([]string, 0, len(meta.Jars)),
	}
	if id := declaredModID(meta.ID); id != "" {
		result.Mods = append(result.Mods, structs.ModInfo{
			ID:           id,
			Name:         meta.Name,
			Version:      meta.Version,
			Description:  meta.Description,
			Dependencies: localModDependenciesFromIDs(requiredFabricDependencyIDs(meta.Depends)),
		})
	}
	for _, jar := range meta.Jars {
		if path := nestedJarPath(jar.File); path != "" {
			result.NestedJars = append(result.NestedJars, path)
		}
	}
	return result, nil
}

// --- NeoForge ---

type neoForgeModParser struct{}

func (neoForgeModParser) Name() string { return "neoforge" }

func (neoForgeModParser) CanParse(r *zip.Reader) bool {
	return zipHasFile(r, "META-INF/neoforge.mods.toml")
}

func (neoForgeModParser) Parse(r *zip.Reader) (parsedModMetadata, error) {
	mods, err := parseModsToml(r, "META-INF/neoforge.mods.toml")
	if err != nil {
		return parsedModMetadata{}, err
	}
	return parsedModMetadata{Mods: mods}, nil
}

// --- Forge ---

type forgeModParser struct{}

func (forgeModParser) Name() string { return "forge" }

func (forgeModParser) CanParse(r *zip.Reader) bool {
	return zipHasFile(r, "META-INF/mods.toml")
}

func (forgeModParser) Parse(r *zip.Reader) (parsedModMetadata, error) {
	mods, err := parseModsToml(r, "META-INF/mods.toml")
	if err != nil {
		return parsedModMetadata{}, err
	}
	return parsedModMetadata{Mods: mods}, nil
}

// --- shared mods.toml parser ---

func parseModsToml(r *zip.Reader, path string) ([]structs.ModInfo, error) {
	data, err := readZipFile(r, path)
	if err != nil {
		return nil, err
	}
	properties := jarManifestProperties(r)

	var meta struct {
		Mods []struct {
			ModID       string `toml:"modId"`
			DisplayName string `toml:"displayName"`
			Version     string `toml:"version"`
			Description string `toml:"description"`
		} `toml:"mods"`
		Dependencies map[string][]struct {
			ModID     string `toml:"modId"`
			Mandatory *bool  `toml:"mandatory"`
			Type      string `toml:"type"`
		} `toml:"dependencies"`
	}
	if err := toml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	infos := make([]structs.ModInfo, 0, len(meta.Mods))
	for _, m := range meta.Mods {
		id := declaredModID(m.ModID)
		if id == "" {
			continue
		}
		infos = append(infos, structs.ModInfo{
			ID:           id,
			Name:         resolveMetadataValue(m.DisplayName, properties),
			Version:      resolveMetadataValue(m.Version, properties),
			Description:  resolveMetadataValue(m.Description, properties),
			Dependencies: localModDependenciesFromIDs(requiredForgeDependencyIDs(meta.Dependencies[id])),
		})
	}
	return infos, nil
}

func requiredFabricDependencyIDs(depends map[string]any) []string {
	if len(depends) == 0 {
		return nil
	}
	ids := make([]string, 0, len(depends))
	for id := range depends {
		id = cleanupDependencyModID(id)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return uniqueDependencyIDs(ids)
}

func requiredForgeDependencyIDs(deps []struct {
	ModID     string `toml:"modId"`
	Mandatory *bool  `toml:"mandatory"`
	Type      string `toml:"type"`
}) []string {
	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		if !forgeDependencyRequired(dep.Mandatory, dep.Type) {
			continue
		}
		id := cleanupDependencyModID(dep.ModID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return uniqueDependencyIDs(ids)
}

func forgeDependencyRequired(mandatory *bool, depType string) bool {
	depType = strings.ToLower(strings.TrimSpace(depType))
	if depType != "" {
		return depType == "required" || depType == "mandatory"
	}
	return mandatory != nil && *mandatory
}

func localModDependenciesFromIDs(ids []string) []structs.LocalModDependency {
	if len(ids) == 0 {
		return nil
	}
	deps := make([]structs.LocalModDependency, 0, len(ids))
	for _, id := range ids {
		deps = append(deps, structs.LocalModDependency{ModID: id, Type: "required"})
	}
	return deps
}

func cleanupDependencyModID(id string) string {
	id = declaredModID(id)
	if id == "" {
		return ""
	}
	switch strings.ToLower(id) {
	case "minecraft", "java", "fabricloader", "quilt_loader", "forge", "neoforge", "modloader":
		return ""
	default:
		return id
	}
}

func uniqueDependencyIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	return out
}

func jarManifestProperties(r *zip.Reader) map[string]string {
	data, err := readZipFile(r, "META-INF/MANIFEST.MF")
	if err != nil {
		return nil
	}

	attrs := parseManifestMainAttributes(data)
	if len(attrs) == 0 {
		return nil
	}

	props := make(map[string]string, len(attrs)+1)
	if version := strings.TrimSpace(attrs["Implementation-Version"]); version != "" {
		props["file.jarVersion"] = version
	}
	for key, value := range attrs {
		if strings.TrimSpace(value) == "" {
			continue
		}
		props["file."+key] = strings.TrimSpace(value)
	}
	return props
}

func parseManifestMainAttributes(data []byte) map[string]string {
	attrs := make(map[string]string)
	var currentKey string
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, " ") && currentKey != "" {
			attrs[currentKey] += line[1:]
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			currentKey = ""
			continue
		}
		currentKey = strings.TrimSpace(key)
		if currentKey == "" {
			continue
		}
		attrs[currentKey] = strings.TrimSpace(value)
	}
	return attrs
}

func resolveMetadataValue(value string, properties map[string]string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	resolved := value
	for key, replacement := range properties {
		resolved = strings.ReplaceAll(resolved, "${"+key+"}", replacement)
	}
	if strings.Contains(resolved, "${") {
		return ""
	}
	return resolved
}

// --- registry ---

var modMetadataParsers = []ModMetadataParser{
	fabricModParser{},
	neoForgeModParser{},
	forgeModParser{},
}

const (
	loaderFabric       = "fabric"
	loaderNeoForge     = "neoforge"
	loaderForge        = "forge"
	jarJarMetadataPath = "META-INF/jarjar/metadata.json"
	maxNestedJarDepth  = 32
)

// --- scanning ---

func isModJar(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jar") || strings.HasSuffix(lower, ".jar.disabled")
}

func StripJarSuffix(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".jar.disabled") {
		return name[:len(name)-len(".jar.disabled")]
	}
	if strings.HasSuffix(lower, ".jar") {
		return name[:len(name)-len(".jar")]
	}
	return name
}

func isJarEnabled(name string) bool {
	return !strings.HasSuffix(strings.ToLower(name), ".jar.disabled")
}

func FileSHA1(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileHashes calculates the SHA1 used by Modrinth and the normalized
// MurmurHash2 fingerprint used by CurseForge.
func FileHashes(path string) (string, uint32) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	h := sha1.New()
	var normalizedLength uint32
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := f.Read(buffer)
		if n > 0 {
			_, _ = h.Write(buffer[:n])
			for _, value := range buffer[:n] {
				if !curseForgeWhitespace(value) {
					normalizedLength++
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0
	}
	return hex.EncodeToString(h.Sum(nil)), curseForgeFingerprint(f, normalizedLength)
}

func curseForgeWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func curseForgeFingerprint(reader io.Reader, normalizedLength uint32) uint32 {
	const multiplier uint32 = 0x5bd1e995
	hash := uint32(1) ^ normalizedLength
	buffer := make([]byte, 32*1024)
	var block [4]byte
	blockSize := 0
	for {
		n, err := reader.Read(buffer)
		for _, value := range buffer[:n] {
			if curseForgeWhitespace(value) {
				continue
			}
			block[blockSize] = value
			blockSize++
			if blockSize == len(block) {
				word := binary.LittleEndian.Uint32(block[:])
				word *= multiplier
				word ^= word >> 24
				word *= multiplier
				hash *= multiplier
				hash ^= word
				blockSize = 0
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return 0
			}
			break
		}
	}
	switch blockSize {
	case 3:
		hash ^= uint32(block[2]) << 16
		fallthrough
	case 2:
		hash ^= uint32(block[1]) << 8
		fallthrough
	case 1:
		hash ^= uint32(block[0])
		hash *= multiplier
	}
	hash ^= hash >> 13
	hash *= multiplier
	hash ^= hash >> 15
	return hash
}

func ScanVersionMods(versionDir string, instanceID string, minecraftVersion string, modLoader string, minecraftDir string) []structs.ModInfo {
	modsDir := filepath.Join(versionDir, "mods")
	return ScanModsDir(modsDir, instanceID, minecraftVersion, modLoader, minecraftDir)
}

func ScanModsDir(modsDir string, instanceID string, minecraftVersion string, modLoader string, pathRoot string) []structs.ModInfo {
	modsDir = strings.TrimSpace(modsDir)
	if modsDir == "" {
		return nil
	}
	if !filepath.IsAbs(modsDir) {
		if abs, err := filepath.Abs(modsDir); err == nil {
			modsDir = abs
		}
	}
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return nil
	}

	var allMods []structs.ModInfo
	for _, entry := range entries {
		if entry.IsDir() || !isModJar(entry.Name()) {
			continue
		}

		jarPath := filepath.Join(modsDir, entry.Name())
		hash, fingerprint := FileHashes(jarPath)
		global.HardlinkIndexAdd(hash, jarPath)
		mods := ParseModJarWithSHA1(jarPath, hash, modLoader)
		enabled := isJarEnabled(entry.Name())
		baseName := StripJarSuffix(entry.Name())

		if len(mods) == 0 {
			continue
		}

		pathValue := jarPath
		if strings.TrimSpace(pathRoot) != "" {
			if rel, err := filepath.Rel(pathRoot, jarPath); err == nil {
				pathValue = rel
			}
		}
		for i := range mods {
			mods[i].FileName = baseName
			mods[i].Path = pathValue
			mods[i].SHA1 = hash
			mods[i].CfFingerprint = fingerprint
			mods[i].Enabled = enabled
			global.UpsertLocalMod(mods[i], instanceID, minecraftVersion, modLoader)
		}
		allMods = append(allMods, mods...)
	}
	return allMods
}

// ScanModFile parses one local mod file and updates the in-memory local index.
func ScanModFile(jarPath, instanceID, minecraftVersion, modLoader, pathRoot string) []structs.ModInfo {
	info, err := os.Stat(jarPath)
	if err != nil || info.IsDir() || !isModJar(filepath.Base(jarPath)) {
		return nil
	}
	hash, fingerprint := FileHashes(jarPath)
	global.HardlinkIndexAdd(hash, jarPath)
	mods := ParseModJarWithSHA1(jarPath, hash, modLoader)
	if len(mods) == 0 {
		return nil
	}
	baseName := StripJarSuffix(filepath.Base(jarPath))
	pathValue := jarPath
	if strings.TrimSpace(pathRoot) != "" {
		if rel, err := filepath.Rel(pathRoot, jarPath); err == nil {
			pathValue = rel
		}
	}
	for i := range mods {
		mods[i].FileName, mods[i].Path, mods[i].SHA1 = baseName, pathValue, hash
		mods[i].CfFingerprint, mods[i].Enabled = fingerprint, isJarEnabled(filepath.Base(jarPath))
		global.UpsertLocalMod(mods[i], instanceID, minecraftVersion, modLoader)
	}
	return mods
}

func parseModJar(jarPath string, modLoader string) []structs.ModInfo {
	return ParseModJarWithSHA1(jarPath, FileSHA1(jarPath), modLoader)
}

func ParseModJarWithSHA1(jarPath string, sha1 string, modLoader string) []structs.ModInfo {
	if mods, ok := global.GetJarMetadata(sha1); ok {
		return mods
	}

	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil
	}
	defer r.Close()

	mods := ParseModZipReader(&r.Reader, filepath.Base(jarPath), modLoader)
	if len(mods) > 0 {
		global.SetJarMetadata(sha1, mods)
	}
	return mods
}

func ParseModZipReader(r *zip.Reader, sourceName string, modLoader string) []structs.ModInfo {
	ctx := modJarParseContext{
		modLoader: normalizeModLoader(modLoader),
		visited:   make(map[[32]byte]struct{}),
	}
	return uniqueModsByID(ctx.parseZipReader(r, sourceName, 0))
}

type modJarParseContext struct {
	modLoader string
	visited   map[[32]byte]struct{}
}

func (ctx modJarParseContext) parseZipReader(r *zip.Reader, sourceName string, depth int) []structs.ModInfo {
	if depth > maxNestedJarDepth {
		logging.Warn("nested mod jar parse depth limit reached", "sourceName", sourceName, "depth", depth)
		return nil
	}

	metadata := ctx.parseDeclaredMetadata(r, sourceName)
	mods := make([]structs.ModInfo, 0, len(metadata.Mods))
	mods = append(mods, metadata.Mods...)

	jijMods := make([]structs.JijModInfo, 0)
	for _, nestedPath := range uniqueNestedJarPaths(metadata.NestedJars) {
		jijMods = append(jijMods, ctx.parseNestedJar(r, nestedPath, sourceName, depth+1)...)
	}
	jijMods = uniqueJijModsByID(jijMods)
	if len(jijMods) > 0 {
		for i := range mods {
			mods[i].JijMods = append([]structs.JijModInfo(nil), jijMods...)
		}
	}
	return mods
}

func (ctx modJarParseContext) parseDeclaredMetadata(r *zip.Reader, sourceName string) parsedModMetadata {
	var out parsedModMetadata
	for _, parser := range ctx.parsers() {
		if !parser.CanParse(r) {
			continue
		}
		metadata, err := parser.Parse(r)
		if err != nil {
			logging.Warn("parse mod metadata failed", "sourceName", sourceName, "parser", parser.Name(), "error", err)
			continue
		}
		out.Mods = append(out.Mods, metadata.Mods...)
		out.NestedJars = append(out.NestedJars, metadata.NestedJars...)
		if parser.Name() == loaderForge || parser.Name() == loaderNeoForge {
			paths, err := parseJarJarNestedPaths(r)
			if err != nil {
				logging.Warn("parse jarjar metadata failed", "sourceName", sourceName, "parser", parser.Name(), "error", err)
				continue
			}
			out.NestedJars = append(out.NestedJars, paths...)
		}
	}
	return out
}

func (ctx modJarParseContext) parsers() []ModMetadataParser {
	if ctx.modLoader == "" {
		return modMetadataParsers
	}
	for _, parser := range modMetadataParsers {
		if parser.Name() == ctx.modLoader {
			return []ModMetadataParser{parser}
		}
	}
	return modMetadataParsers
}

func (ctx modJarParseContext) parseNestedJar(r *zip.Reader, nestedPath string, sourceName string, depth int) []structs.JijModInfo {
	data, err := readZipFile(r, nestedPath)
	if err != nil {
		logging.Warn("read nested mod jar failed", "sourceName", sourceName, "nestedPath", nestedPath, "error", err)
		return nil
	}

	sum := sha256.Sum256(data)
	if _, ok := ctx.visited[sum]; ok {
		return nil
	}
	ctx.visited[sum] = struct{}{}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		logging.Warn("open nested mod jar failed", "sourceName", sourceName, "nestedPath", nestedPath, "error", err)
		return nil
	}
	nestedMods := ctx.parseZipReader(zr, sourceName+" > "+nestedPath, depth)
	result := make([]structs.JijModInfo, 0, len(nestedMods))
	for _, mod := range nestedMods {
		if id := declaredModID(mod.ID); id != "" {
			result = append(result, structs.JijModInfo{
				ID:   id,
				Name: mod.Name,
			})
		}
		result = append(result, mod.JijMods...)
	}
	return uniqueJijModsByID(result)
}

func parseJarJarNestedPaths(r *zip.Reader) ([]string, error) {
	if !zipHasFile(r, jarJarMetadataPath) {
		return nil, nil
	}
	data, err := readZipFile(r, jarJarMetadataPath)
	if err != nil {
		return nil, err
	}

	var meta struct {
		Jars []struct {
			Path string `json:"path"`
		} `json:"jars"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", jarJarMetadataPath, err)
	}

	paths := make([]string, 0, len(meta.Jars))
	for _, jar := range meta.Jars {
		if path := nestedJarPath(jar.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func normalizeModLoader(modLoader string) string {
	switch strings.ToLower(strings.TrimSpace(modLoader)) {
	case loaderFabric:
		return loaderFabric
	case loaderForge:
		return loaderForge
	case loaderNeoForge:
		return loaderNeoForge
	default:
		return ""
	}
}

func declaredModID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "${") {
		return ""
	}
	return id
}

func nestedJarPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "/")
	return path
}

func uniqueNestedJarPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = nestedJarPath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func uniqueModsByID(mods []structs.ModInfo) []structs.ModInfo {
	seen := make(map[string]struct{}, len(mods))
	out := make([]structs.ModInfo, 0, len(mods))
	for _, mod := range mods {
		mod.ID = declaredModID(mod.ID)
		if mod.ID == "" {
			continue
		}
		key := strings.ToLower(mod.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, mod)
	}
	return out
}

func uniqueJijModsByID(mods []structs.JijModInfo) []structs.JijModInfo {
	seen := make(map[string]struct{}, len(mods))
	out := make([]structs.JijModInfo, 0, len(mods))
	for _, mod := range mods {
		mod.ID = declaredModID(mod.ID)
		if mod.ID == "" {
			continue
		}
		key := strings.ToLower(mod.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, mod)
	}
	return out
}

// PrimaryModIDs returns lowercased, deduplicated mod IDs declared directly by
// the JAR. JIJ entries are stored under ModInfo.JijMods and are intentionally
// not included.
func PrimaryModIDs(mods []structs.ModInfo) []string {
	seen := make(map[string]struct{}, len(mods))
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		id := strings.ToLower(strings.TrimSpace(m.ID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// --- zip helpers ---

func zipHasFile(r *zip.Reader, name string) bool {
	for _, f := range r.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func readZipFile(r *zip.Reader, name string) ([]byte, error) {
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("file not found in archive: %s", name)
}

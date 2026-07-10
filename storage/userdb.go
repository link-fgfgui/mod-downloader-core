package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type userStore struct {
	db   *sql.DB
	path string
}

const sqliteDriverName = "sqlite"
const favoriteRefMaxDepth = 32

var errFavoriteListRefCycle = errors.New("favorite list reference would create a cycle")

func openUserStore(path string) (*userStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("empty user database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &userStore{db: db, path: path}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *userStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *userStore) ensureSchema() error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pinned_mods (
			id TEXT PRIMARY KEY,
			platform TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			version_id TEXT NOT NULL,
			minecraft_version TEXT NOT NULL,
			mod_loader TEXT NOT NULL,
			UNIQUE(platform, mod_id, minecraft_version, mod_loader)
		)`,
		`CREATE TABLE IF NOT EXISTS favorite_lists (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			sort_order INTEGER NOT NULL,
			system INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS favorite_mods (
			id TEXT PRIMARY KEY,
			list_id TEXT NOT NULL REFERENCES favorite_lists(id) ON DELETE CASCADE,
			platform TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			version_id TEXT NOT NULL DEFAULT '',
			minecraft_version TEXT NOT NULL DEFAULT '',
			mod_loader TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			slug TEXT NOT NULL DEFAULT '',
			icon_url TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			categories_json TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(list_id, platform, mod_id, minecraft_version, mod_loader)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_stats (
			key TEXT PRIMARY KEY,
			value INTEGER NOT NULL DEFAULT 0 CHECK(value >= 0),
			updated_at INTEGER NOT NULL
		)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, unixepoch())`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := s.ensureSchemaV2(); err != nil {
		return err
	}
	return nil
}

func (s *userStore) ensureSchemaV2() error {
	columns := map[string]string{
		"group_id":   "TEXT NOT NULL DEFAULT ''",
		"icon_kind":  "TEXT NOT NULL DEFAULT ''",
		"icon_value": "TEXT NOT NULL DEFAULT ''",
		"icon_url":   "TEXT NOT NULL DEFAULT ''",
		"pinned":     "INTEGER NOT NULL DEFAULT 0",
	}
	for name, definition := range columns {
		ok, err := s.columnExists("favorite_lists", name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE favorite_lists ADD COLUMN ` + name + ` ` + definition); err != nil {
			return err
		}
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS favorite_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			sort_order INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS favorite_list_refs (
			id TEXT PRIMARY KEY,
			parent_list_id TEXT NOT NULL REFERENCES favorite_lists(id) ON DELETE CASCADE,
			child_list_id TEXT NOT NULL REFERENCES favorite_lists(id) ON DELETE CASCADE,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			sort_order INTEGER NOT NULL,
			UNIQUE(parent_list_id, child_list_id),
			CHECK(parent_list_id <> child_list_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_lists_pinned_order ON favorite_lists(pinned, sort_order, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_lists_group_order ON favorite_lists(group_id, sort_order, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_groups_order ON favorite_groups(sort_order, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_list_refs_parent_order ON favorite_list_refs(parent_list_id, sort_order)`,
		`CREATE INDEX IF NOT EXISTS idx_favorite_list_refs_child ON favorite_list_refs(child_list_id)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (2, unixepoch())`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *userStore) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *userStore) upsertPinnedMod(p PinnedMod) error {
	p = normalizePinnedMod(p)
	if p.Platform == "" || p.ModID == "" || p.VersionID == "" || p.MinecraftVersion == "" || p.ModLoader == "" {
		return nil
	}
	if existing, ok, err := s.getPinnedMod(p.Platform, p.ModID, p.MinecraftVersion, p.ModLoader); err != nil {
		return err
	} else if ok && existing.ID != "" {
		p.ID = existing.ID
	}
	if p.ID == "" {
		p.ID = NewID()
	}
	_, err := s.db.Exec(
		`INSERT INTO pinned_mods(id, platform, mod_id, version_id, minecraft_version, mod_loader)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(platform, mod_id, minecraft_version, mod_loader)
		 DO UPDATE SET version_id = excluded.version_id`,
		p.ID, p.Platform, p.ModID, p.VersionID, p.MinecraftVersion, p.ModLoader,
	)
	return err
}

func (s *userStore) getPinnedMod(platform, modID, mcVersion, modLoader string) (PinnedMod, bool, error) {
	key := makePinnedModKey(platform, modID, mcVersion, modLoader)
	if key.Platform == "" || key.ModID == "" || key.MinecraftVersion == "" || key.ModLoader == "" {
		return PinnedMod{}, false, nil
	}

	var p PinnedMod
	err := s.db.QueryRow(
		`SELECT id, platform, mod_id, version_id, minecraft_version, mod_loader
		 FROM pinned_mods
		 WHERE platform = ? AND mod_id = ? AND minecraft_version = ? AND mod_loader = ?`,
		key.Platform, key.ModID, key.MinecraftVersion, key.ModLoader,
	).Scan(&p.ID, &p.Platform, &p.ModID, &p.VersionID, &p.MinecraftVersion, &p.ModLoader)
	if errors.Is(err, sql.ErrNoRows) {
		return PinnedMod{}, false, nil
	}
	if err != nil {
		return PinnedMod{}, false, err
	}
	return p, true, nil
}

func (s *userStore) deletePinnedMod(platform, modID, mcVersion, modLoader string) error {
	key := makePinnedModKey(platform, modID, mcVersion, modLoader)
	if key.Platform == "" || key.ModID == "" || key.MinecraftVersion == "" || key.ModLoader == "" {
		return nil
	}
	_, err := s.db.Exec(
		`DELETE FROM pinned_mods
		 WHERE platform = ? AND mod_id = ? AND minecraft_version = ? AND mod_loader = ?`,
		key.Platform, key.ModID, key.MinecraftVersion, key.ModLoader,
	)
	return err
}

func (s *userStore) listPinnedMods() ([]PinnedMod, error) {
	rows, err := s.db.Query(
		`SELECT id, platform, mod_id, version_id, minecraft_version, mod_loader
		 FROM pinned_mods
		 ORDER BY platform ASC, mod_id ASC, minecraft_version ASC, mod_loader ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pins []PinnedMod
	for rows.Next() {
		var p PinnedMod
		if err := rows.Scan(&p.ID, &p.Platform, &p.ModID, &p.VersionID, &p.MinecraftVersion, &p.ModLoader); err != nil {
			return nil, err
		}
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

func (s *userStore) createFavoriteList(name string) (FavoriteList, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return FavoriteList{}, nil
	}
	now := time.Now().Unix()
	var maxSortOrder int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM favorite_lists`).Scan(&maxSortOrder); err != nil {
		return FavoriteList{}, err
	}
	list := FavoriteList{
		ID:        NewID(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
		SortOrder: maxSortOrder + 1,
	}
	_, err := s.db.Exec(
		`INSERT INTO favorite_lists(id, name, created_at, updated_at, sort_order, system)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		list.ID, list.Name, list.CreatedAt, list.UpdatedAt, list.SortOrder, boolToInt(list.System),
	)
	if err != nil {
		return FavoriteList{}, err
	}
	return list, nil
}

func (s *userStore) updateFavoriteListName(id, name string) (FavoriteList, bool, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return FavoriteList{}, false, nil
	}
	now := time.Now().Unix()
	result, err := s.db.Exec(`UPDATE favorite_lists SET name = ?, updated_at = ? WHERE id = ?`, name, now, id)
	if err != nil {
		return FavoriteList{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return FavoriteList{}, false, err
	}
	if affected == 0 {
		return FavoriteList{}, false, nil
	}
	list, ok, err := s.getFavoriteList(id)
	return list, ok, err
}

func (s *userStore) getFavoriteList(id string) (FavoriteList, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FavoriteList{}, false, nil
	}
	list, err := scanFavoriteList(s.db.QueryRow(
		`SELECT id, group_id, name, icon_kind, icon_value, icon_url, pinned, created_at, updated_at, sort_order, system
		 FROM favorite_lists WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return FavoriteList{}, false, nil
	}
	if err != nil {
		return FavoriteList{}, false, err
	}
	return list, true, nil
}

func (s *userStore) deleteFavoriteList(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	result, err := s.db.Exec(`DELETE FROM favorite_lists WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *userStore) listFavoriteLists() ([]FavoriteList, error) {
	rows, err := s.db.Query(
		`SELECT id, group_id, name, icon_kind, icon_value, icon_url, pinned, created_at, updated_at, sort_order, system
		 FROM favorite_lists
		 ORDER BY pinned DESC, sort_order ASC, created_at ASC, name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lists []FavoriteList
	for rows.Next() {
		list, err := scanFavoriteList(rows)
		if err != nil {
			return nil, err
		}
		lists = append(lists, list)
	}
	return lists, rows.Err()
}

func (s *userStore) updateFavoriteListMetadata(list FavoriteList) (FavoriteList, bool, error) {
	list = normalizeFavoriteList(list)
	if list.ID == "" {
		return FavoriteList{}, false, nil
	}
	existing, ok, err := s.getFavoriteList(list.ID)
	if err != nil {
		return FavoriteList{}, false, err
	}
	if !ok {
		return FavoriteList{}, false, nil
	}
	if list.SortOrder == 0 {
		list.SortOrder = existing.SortOrder
	}
	now := time.Now().Unix()
	result, err := s.db.Exec(
		`UPDATE favorite_lists
		 SET group_id = ?, icon_kind = ?, icon_value = ?, icon_url = ?, pinned = ?, sort_order = ?, updated_at = ?
		 WHERE id = ?`,
		list.GroupID, list.IconKind, list.IconValue, list.IconURL, boolToInt(list.Pinned), list.SortOrder, now, list.ID,
	)
	if err != nil {
		return FavoriteList{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return FavoriteList{}, false, err
	}
	if affected == 0 {
		return FavoriteList{}, false, nil
	}
	return s.getFavoriteList(list.ID)
}

func (s *userStore) reorderFavoriteLists(ids []string) error {
	return s.reorderRows("favorite_lists", ids)
}

type favoriteListScanner interface {
	Scan(dest ...any) error
}

func scanFavoriteList(scanner favoriteListScanner) (FavoriteList, error) {
	var list FavoriteList
	var pinned, system int
	if err := scanner.Scan(
		&list.ID,
		&list.GroupID,
		&list.Name,
		&list.IconKind,
		&list.IconValue,
		&list.IconURL,
		&pinned,
		&list.CreatedAt,
		&list.UpdatedAt,
		&list.SortOrder,
		&system,
	); err != nil {
		return FavoriteList{}, err
	}
	list.Pinned = pinned != 0
	list.System = system != 0
	return normalizeFavoriteList(list), nil
}

func (s *userStore) createFavoriteGroup(name string) (FavoriteGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return FavoriteGroup{}, nil
	}
	now := time.Now().Unix()
	var maxSortOrder int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM favorite_groups`).Scan(&maxSortOrder); err != nil {
		return FavoriteGroup{}, err
	}
	group := FavoriteGroup{
		ID:        NewID(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
		SortOrder: maxSortOrder + 1,
	}
	_, err := s.db.Exec(
		`INSERT INTO favorite_groups(id, name, created_at, updated_at, sort_order)
		 VALUES (?, ?, ?, ?, ?)`,
		group.ID, group.Name, group.CreatedAt, group.UpdatedAt, group.SortOrder,
	)
	if err != nil {
		return FavoriteGroup{}, err
	}
	return group, nil
}

func (s *userStore) updateFavoriteGroupName(id, name string) (FavoriteGroup, bool, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return FavoriteGroup{}, false, nil
	}
	now := time.Now().Unix()
	result, err := s.db.Exec(`UPDATE favorite_groups SET name = ?, updated_at = ? WHERE id = ?`, name, now, id)
	if err != nil {
		return FavoriteGroup{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return FavoriteGroup{}, false, err
	}
	if affected == 0 {
		return FavoriteGroup{}, false, nil
	}
	return s.getFavoriteGroup(id)
}

func (s *userStore) getFavoriteGroup(id string) (FavoriteGroup, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FavoriteGroup{}, false, nil
	}
	group, err := scanFavoriteGroup(s.db.QueryRow(
		`SELECT id, name, created_at, updated_at, sort_order
		 FROM favorite_groups WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return FavoriteGroup{}, false, nil
	}
	if err != nil {
		return FavoriteGroup{}, false, err
	}
	return group, true, nil
}

func (s *userStore) deleteFavoriteGroup(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`UPDATE favorite_lists SET group_id = '', updated_at = ? WHERE group_id = ?`, time.Now().Unix(), id); err != nil {
		return false, err
	}
	result, err := tx.Exec(`DELETE FROM favorite_groups WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	return affected > 0, nil
}

func (s *userStore) listFavoriteGroups() ([]FavoriteGroup, error) {
	rows, err := s.db.Query(
		`SELECT id, name, created_at, updated_at, sort_order
		 FROM favorite_groups
		 ORDER BY sort_order ASC, created_at ASC, name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []FavoriteGroup
	for rows.Next() {
		group, err := scanFavoriteGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *userStore) reorderFavoriteGroups(ids []string) error {
	return s.reorderRows("favorite_groups", ids)
}

func (s *userStore) reorderRows(table string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().Unix()
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.Exec(`UPDATE `+table+` SET sort_order = ?, updated_at = ? WHERE id = ?`, i+1, now, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type favoriteGroupScanner interface {
	Scan(dest ...any) error
}

func scanFavoriteGroup(scanner favoriteGroupScanner) (FavoriteGroup, error) {
	var group FavoriteGroup
	if err := scanner.Scan(&group.ID, &group.Name, &group.CreatedAt, &group.UpdatedAt, &group.SortOrder); err != nil {
		return FavoriteGroup{}, err
	}
	return normalizeFavoriteGroup(group), nil
}

func (s *userStore) upsertFavoriteMod(mod FavoriteMod) (FavoriteMod, bool, error) {
	mod = normalizeFavoriteMod(mod)
	if mod.ListID == "" || mod.Platform == "" || mod.ModID == "" {
		return FavoriteMod{}, false, nil
	}
	if _, ok, err := s.getFavoriteList(mod.ListID); err != nil {
		return FavoriteMod{}, false, err
	} else if !ok {
		return FavoriteMod{}, false, nil
	}

	if existing, ok, err := s.getFavoriteModByKey(mod.ListID, mod.Platform, mod.ModID, mod.MinecraftVersion, mod.ModLoader); err != nil {
		return FavoriteMod{}, false, err
	} else if ok {
		if mod.ID == "" {
			mod.ID = existing.ID
		}
		if mod.CreatedAt == 0 {
			mod.CreatedAt = existing.CreatedAt
		}
	}

	now := time.Now().Unix()
	if mod.ID == "" {
		mod.ID = NewID()
	}
	if mod.CreatedAt == 0 {
		mod.CreatedAt = now
	}
	mod.UpdatedAt = now
	categoriesJSON, err := favoriteCategoriesJSON(mod.Categories)
	if err != nil {
		return FavoriteMod{}, false, err
	}
	_, err = s.db.Exec(
		`INSERT INTO favorite_mods(
			id, list_id, platform, mod_id, version_id, minecraft_version, mod_loader,
			title, slug, icon_url, description, categories_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(list_id, platform, mod_id, minecraft_version, mod_loader)
		DO UPDATE SET
			id = excluded.id,
			version_id = excluded.version_id,
			title = excluded.title,
			slug = excluded.slug,
			icon_url = excluded.icon_url,
			description = excluded.description,
			categories_json = excluded.categories_json,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		mod.ID, mod.ListID, mod.Platform, mod.ModID, mod.VersionID, mod.MinecraftVersion, mod.ModLoader,
		mod.Title, mod.Slug, mod.IconURL, mod.Description, categoriesJSON, mod.CreatedAt, mod.UpdatedAt,
	)
	if err != nil {
		return FavoriteMod{}, false, err
	}
	return copyFavoriteMod(mod), true, nil
}

func (s *userStore) getFavoriteModByKey(listID, platform, modID, mcVersion, modLoader string) (FavoriteMod, bool, error) {
	key := makeFavoriteModKey(listID, platform, modID, mcVersion, modLoader)
	if key.ListID == "" || key.Platform == "" || key.ModID == "" {
		return FavoriteMod{}, false, nil
	}
	return s.getFavoriteMod(
		`SELECT id, list_id, platform, mod_id, version_id, minecraft_version, mod_loader,
			title, slug, icon_url, description, categories_json, created_at, updated_at
		 FROM favorite_mods
		 WHERE list_id = ? AND platform = ? AND mod_id = ? AND minecraft_version = ? AND mod_loader = ?`,
		key.ListID, key.Platform, key.ModID, key.MinecraftVersion, key.ModLoader,
	)
}

func (s *userStore) deleteFavoriteMod(listID, platform, modID, mcVersion, modLoader string) (bool, error) {
	key := makeFavoriteModKey(listID, platform, modID, mcVersion, modLoader)
	if key.ListID == "" || key.Platform == "" || key.ModID == "" {
		return false, nil
	}
	result, err := s.db.Exec(
		`DELETE FROM favorite_mods
		 WHERE list_id = ? AND platform = ? AND mod_id = ? AND minecraft_version = ? AND mod_loader = ?`,
		key.ListID, key.Platform, key.ModID, key.MinecraftVersion, key.ModLoader,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *userStore) listFavoriteMods(listID string) ([]FavoriteMod, error) {
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, list_id, platform, mod_id, version_id, minecraft_version, mod_loader,
			title, slug, icon_url, description, categories_json, created_at, updated_at
		 FROM favorite_mods
		 WHERE list_id = ?`,
		listID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mods []FavoriteMod
	for rows.Next() {
		mod, err := scanFavoriteMod(rows)
		if err != nil {
			return nil, err
		}
		mods = append(mods, mod)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(mods, func(i, j int) bool {
		if mods[i].CreatedAt != mods[j].CreatedAt {
			return mods[i].CreatedAt < mods[j].CreatedAt
		}
		if mods[i].Platform != mods[j].Platform {
			return mods[i].Platform < mods[j].Platform
		}
		return mods[i].ModID < mods[j].ModID
	})
	return mods, nil
}

func (s *userStore) createFavoriteListRef(parentListID, childListID string) (FavoriteListRef, bool, error) {
	ref := normalizeFavoriteListRef(FavoriteListRef{
		ParentListID: parentListID,
		ChildListID:  childListID,
	})
	if ref.ParentListID == "" || ref.ChildListID == "" {
		return FavoriteListRef{}, false, nil
	}
	if ref.ParentListID == ref.ChildListID {
		return FavoriteListRef{}, false, errFavoriteListRefCycle
	}
	if _, ok, err := s.getFavoriteList(ref.ParentListID); err != nil {
		return FavoriteListRef{}, false, err
	} else if !ok {
		return FavoriteListRef{}, false, nil
	}
	if _, ok, err := s.getFavoriteList(ref.ChildListID); err != nil {
		return FavoriteListRef{}, false, err
	} else if !ok {
		return FavoriteListRef{}, false, nil
	}
	if existing, ok, err := s.getFavoriteListRef(ref.ParentListID, ref.ChildListID); err != nil {
		return FavoriteListRef{}, false, err
	} else if ok {
		return existing, true, nil
	}
	reachesParent, err := s.favoriteListCanReach(ref.ChildListID, ref.ParentListID)
	if err != nil {
		return FavoriteListRef{}, false, err
	}
	if reachesParent {
		return FavoriteListRef{}, false, errFavoriteListRefCycle
	}

	now := time.Now().Unix()
	var maxSortOrder int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM favorite_list_refs WHERE parent_list_id = ?`, ref.ParentListID).Scan(&maxSortOrder); err != nil {
		return FavoriteListRef{}, false, err
	}
	ref.ID = NewID()
	ref.CreatedAt = now
	ref.UpdatedAt = now
	ref.SortOrder = maxSortOrder + 1
	_, err = s.db.Exec(
		`INSERT INTO favorite_list_refs(id, parent_list_id, child_list_id, created_at, updated_at, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ref.ID, ref.ParentListID, ref.ChildListID, ref.CreatedAt, ref.UpdatedAt, ref.SortOrder,
	)
	if err != nil {
		return FavoriteListRef{}, false, err
	}
	return ref, true, nil
}

func (s *userStore) getFavoriteListRef(parentListID, childListID string) (FavoriteListRef, bool, error) {
	parentListID = strings.TrimSpace(parentListID)
	childListID = strings.TrimSpace(childListID)
	if parentListID == "" || childListID == "" {
		return FavoriteListRef{}, false, nil
	}
	ref, err := scanFavoriteListRef(s.db.QueryRow(
		`SELECT id, parent_list_id, child_list_id, created_at, updated_at, sort_order
		 FROM favorite_list_refs
		 WHERE parent_list_id = ? AND child_list_id = ?`,
		parentListID, childListID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return FavoriteListRef{}, false, nil
	}
	if err != nil {
		return FavoriteListRef{}, false, err
	}
	return ref, true, nil
}

func (s *userStore) deleteFavoriteListRef(parentListID, childListID string) (bool, error) {
	parentListID = strings.TrimSpace(parentListID)
	childListID = strings.TrimSpace(childListID)
	if parentListID == "" || childListID == "" {
		return false, nil
	}
	result, err := s.db.Exec(
		`DELETE FROM favorite_list_refs
		 WHERE parent_list_id = ? AND child_list_id = ?`,
		parentListID, childListID,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *userStore) listFavoriteListRefs(parentListID string) ([]FavoriteListRef, error) {
	parentListID = strings.TrimSpace(parentListID)
	if parentListID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, parent_list_id, child_list_id, created_at, updated_at, sort_order
		 FROM favorite_list_refs
		 WHERE parent_list_id = ?
		 ORDER BY sort_order ASC, created_at ASC`,
		parentListID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []FavoriteListRef
	for rows.Next() {
		ref, err := scanFavoriteListRef(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *userStore) favoriteListCanReach(startListID, targetListID string) (bool, error) {
	startListID = strings.TrimSpace(startListID)
	targetListID = strings.TrimSpace(targetListID)
	if startListID == "" || targetListID == "" {
		return false, nil
	}
	var found int
	err := s.db.QueryRow(
		`WITH RECURSIVE reachable(list_id, depth, path) AS (
			SELECT ? AS list_id, 0 AS depth, ',' || ? || ',' AS path
			UNION ALL
			SELECT refs.child_list_id, reachable.depth + 1, reachable.path || refs.child_list_id || ','
			FROM favorite_list_refs refs
			JOIN reachable ON refs.parent_list_id = reachable.list_id
			WHERE reachable.depth < ?
			  AND instr(reachable.path, ',' || refs.child_list_id || ',') = 0
		)
		SELECT 1 FROM reachable WHERE list_id = ? LIMIT 1`,
		startListID, startListID, favoriteRefMaxDepth, targetListID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == 1, nil
}

func (s *userStore) listFavoriteContents(listID string) (FavoriteListContents, error) {
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return FavoriteListContents{}, nil
	}
	rows, err := s.db.Query(
		`WITH RECURSIVE reachable(list_id, depth, path) AS (
			SELECT ? AS list_id, 0 AS depth, ',' || ? || ',' AS path
			UNION ALL
			SELECT refs.child_list_id, reachable.depth + 1, reachable.path || refs.child_list_id || ','
			FROM favorite_list_refs refs
			JOIN reachable ON refs.parent_list_id = reachable.list_id
			WHERE reachable.depth < ?
			  AND instr(reachable.path, ',' || refs.child_list_id || ',') = 0
		)
		SELECT fm.id, fm.list_id, fm.platform, fm.mod_id, fm.version_id, fm.minecraft_version, fm.mod_loader,
			fm.title, fm.slug, fm.icon_url, fm.description, fm.categories_json, fm.created_at, fm.updated_at,
			reachable.depth, fl.name
		FROM favorite_mods fm
		JOIN reachable ON fm.list_id = reachable.list_id
		JOIN favorite_lists fl ON fl.id = fm.list_id
		ORDER BY reachable.depth ASC, fm.created_at ASC, fm.platform ASC, fm.mod_id ASC`,
		listID, listID, favoriteRefMaxDepth,
	)
	if err != nil {
		return FavoriteListContents{}, err
	}
	defer rows.Close()

	seen := make(map[favoriteModKey]struct{})
	var mods []FavoriteModEntry
	for rows.Next() {
		entry, err := scanFavoriteModEntry(rows)
		if err != nil {
			return FavoriteListContents{}, err
		}
		key := makeFavoriteModKey(entry.ListID, entry.Platform, entry.ModID, entry.MinecraftVersion, entry.ModLoader)
		key.ListID = listID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		mods = append(mods, entry)
	}
	if err := rows.Err(); err != nil {
		return FavoriteListContents{}, err
	}
	refs, err := s.listFavoriteListRefs(listID)
	if err != nil {
		return FavoriteListContents{}, err
	}
	return copyFavoriteListContents(FavoriteListContents{ListID: listID, Mods: mods, Refs: refs}), nil
}

type favoriteListRefScanner interface {
	Scan(dest ...any) error
}

func scanFavoriteListRef(scanner favoriteListRefScanner) (FavoriteListRef, error) {
	var ref FavoriteListRef
	if err := scanner.Scan(&ref.ID, &ref.ParentListID, &ref.ChildListID, &ref.CreatedAt, &ref.UpdatedAt, &ref.SortOrder); err != nil {
		return FavoriteListRef{}, err
	}
	return normalizeFavoriteListRef(ref), nil
}

func scanFavoriteModEntry(scanner favoriteModScanner) (FavoriteModEntry, error) {
	var entry FavoriteModEntry
	var categoriesJSON string
	var depth int
	if err := scanner.Scan(
		&entry.ID,
		&entry.ListID,
		&entry.Platform,
		&entry.ModID,
		&entry.VersionID,
		&entry.MinecraftVersion,
		&entry.ModLoader,
		&entry.Title,
		&entry.Slug,
		&entry.IconURL,
		&entry.Description,
		&categoriesJSON,
		&entry.CreatedAt,
		&entry.UpdatedAt,
		&depth,
		&entry.SourceListName,
	); err != nil {
		return FavoriteModEntry{}, err
	}
	if categoriesJSON != "" {
		if err := json.Unmarshal([]byte(categoriesJSON), &entry.Categories); err != nil {
			return FavoriteModEntry{}, fmt.Errorf("decode favorite categories: %w", err)
		}
	}
	entry.Categories = copyStringSlice(entry.Categories)
	entry.SourceListID = entry.ListID
	entry.Referenced = depth > 0
	return copyFavoriteModEntry(entry), nil
}

func (s *userStore) getFavoriteMod(query string, args ...any) (FavoriteMod, bool, error) {
	row := s.db.QueryRow(query, args...)
	mod, err := scanFavoriteMod(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FavoriteMod{}, false, nil
	}
	if err != nil {
		return FavoriteMod{}, false, err
	}
	return mod, true, nil
}

type favoriteModScanner interface {
	Scan(dest ...any) error
}

func scanFavoriteMod(scanner favoriteModScanner) (FavoriteMod, error) {
	var mod FavoriteMod
	var categoriesJSON string
	if err := scanner.Scan(
		&mod.ID,
		&mod.ListID,
		&mod.Platform,
		&mod.ModID,
		&mod.VersionID,
		&mod.MinecraftVersion,
		&mod.ModLoader,
		&mod.Title,
		&mod.Slug,
		&mod.IconURL,
		&mod.Description,
		&categoriesJSON,
		&mod.CreatedAt,
		&mod.UpdatedAt,
	); err != nil {
		return FavoriteMod{}, err
	}
	if categoriesJSON != "" {
		if err := json.Unmarshal([]byte(categoriesJSON), &mod.Categories); err != nil {
			return FavoriteMod{}, fmt.Errorf("decode favorite categories: %w", err)
		}
	}
	mod.Categories = copyStringSlice(mod.Categories)
	return mod, nil
}

func favoriteCategoriesJSON(categories []string) (string, error) {
	categories = copyStringSlice(categories)
	if len(categories) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(categories)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *userStore) incrementUsageStat(key UsageStatKey, delta int64) error {
	_, err := s.db.Exec(
		`INSERT INTO usage_stats(key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
			value = usage_stats.value + excluded.value,
			updated_at = excluded.updated_at`,
		string(key), delta, time.Now().Unix(),
	)
	return err
}

func (s *userStore) usageStatValues() (map[UsageStatKey]int64, error) {
	rows, err := s.db.Query(`SELECT key, value FROM usage_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make(map[UsageStatKey]int64)
	for rows.Next() {
		var key UsageStatKey
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

package database

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
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, unixepoch())`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *userStore) migrateLegacyUserData(state *cacheState) error {
	if state == nil || (len(state.PinnedMods) == 0 && len(state.FavoriteLists) == 0 && len(state.FavoriteMods) == 0) {
		return nil
	}

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

	for _, pin := range state.PinnedMods {
		pin = normalizePinnedMod(pin)
		if pin.Platform == "" || pin.ModID == "" || pin.VersionID == "" || pin.MinecraftVersion == "" || pin.ModLoader == "" {
			continue
		}
		if pin.ID == "" {
			pin.ID = NewID()
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO pinned_mods(id, platform, mod_id, version_id, minecraft_version, mod_loader)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			pin.ID, pin.Platform, pin.ModID, pin.VersionID, pin.MinecraftVersion, pin.ModLoader,
		); err != nil {
			return err
		}
	}

	for _, list := range state.FavoriteLists {
		list = normalizeFavoriteList(list)
		if list.ID == "" || list.Name == "" {
			continue
		}
		if list.CreatedAt == 0 {
			list.CreatedAt = time.Now().Unix()
		}
		if list.UpdatedAt == 0 {
			list.UpdatedAt = list.CreatedAt
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO favorite_lists(id, name, created_at, updated_at, sort_order, system)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			list.ID, list.Name, list.CreatedAt, list.UpdatedAt, list.SortOrder, boolToInt(list.System),
		); err != nil {
			return err
		}
	}

	for _, mod := range state.FavoriteMods {
		mod = normalizeFavoriteMod(mod)
		if mod.ListID == "" || mod.Platform == "" || mod.ModID == "" {
			continue
		}
		listExists, err := txFavoriteListExists(tx, mod.ListID)
		if err != nil {
			return err
		}
		if !listExists {
			continue
		}
		if mod.ID == "" {
			mod.ID = NewID()
		}
		if mod.CreatedAt == 0 {
			mod.CreatedAt = time.Now().Unix()
		}
		if mod.UpdatedAt == 0 {
			mod.UpdatedAt = mod.CreatedAt
		}
		categoriesJSON, err := favoriteCategoriesJSON(mod.Categories)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO favorite_mods(
				id, list_id, platform, mod_id, version_id, minecraft_version, mod_loader,
				title, slug, icon_url, description, categories_json, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mod.ID, mod.ListID, mod.Platform, mod.ModID, mod.VersionID, mod.MinecraftVersion, mod.ModLoader,
			mod.Title, mod.Slug, mod.IconURL, mod.Description, categoriesJSON, mod.CreatedAt, mod.UpdatedAt,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	state.clearLegacyUserData()
	return nil
}

func txFavoriteListExists(tx *sql.Tx, id string) (bool, error) {
	var value int
	err := tx.QueryRow(`SELECT 1 FROM favorite_lists WHERE id = ?`, id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
	var list FavoriteList
	var system int
	err := s.db.QueryRow(
		`SELECT id, name, created_at, updated_at, sort_order, system FROM favorite_lists WHERE id = ?`,
		id,
	).Scan(&list.ID, &list.Name, &list.CreatedAt, &list.UpdatedAt, &list.SortOrder, &system)
	if errors.Is(err, sql.ErrNoRows) {
		return FavoriteList{}, false, nil
	}
	if err != nil {
		return FavoriteList{}, false, err
	}
	list.System = system != 0
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
		`SELECT id, name, created_at, updated_at, sort_order, system
		 FROM favorite_lists
		 ORDER BY sort_order ASC, created_at ASC, name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lists []FavoriteList
	for rows.Next() {
		var list FavoriteList
		var system int
		if err := rows.Scan(&list.ID, &list.Name, &list.CreatedAt, &list.UpdatedAt, &list.SortOrder, &system); err != nil {
			return nil, err
		}
		list.System = system != 0
		lists = append(lists, list)
	}
	return lists, rows.Err()
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

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

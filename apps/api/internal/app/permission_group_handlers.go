package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (a *App) handlePermissionCatalog(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"items": permissionCatalog()})
}

func (a *App) handleDefaultPermissionLimits(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, defaultPermissionLimits())
}

func (a *App) handleListPermissionGroups(w http.ResponseWriter, r *http.Request) {
	systemColumn := permissionGroupSystemColumnSQL(databaseDriverSQLite)
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name,description,permissions_json,limits_json,`+systemColumn+`,created_at,updated_at
		FROM permission_groups
		ORDER BY created_at ASC,name ASC`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list permission groups")
		return
	}

	items := []PermissionGroup{}
	for rows.Next() {
		var item PermissionGroup
		var rawPermissions, rawLimits, created, updated string
		var system int
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &rawPermissions, &rawLimits, &system, &created, &updated); err != nil {
			rows.Close()
			respondError(w, http.StatusInternalServerError, "failed to scan permission groups")
			return
		}
		item.Permissions = decodeStoredPermissions(rawPermissions)
		item.Limits = decodeStoredLimits(rawLimits)
		item.System = intBool(system)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		respondError(w, http.StatusInternalServerError, "failed to list permission groups")
		return
	}
	if err := rows.Close(); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list permission groups")
		return
	}
	var adminCount, regularCount int
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&adminCount); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to count users")
		return
	}
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users u
		WHERE u.role<>'admin'
		  AND NOT EXISTS (
			SELECT 1 FROM user_permission_groups upg
			WHERE upg.user_id=u.id AND upg.group_id NOT IN (?,?)
		  )`, PermissionGroupSuperAdmin, PermissionGroupRegular,
	).Scan(&regularCount); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to count users")
		return
	}
	for i := range items {
		switch items[i].ID {
		case PermissionGroupSuperAdmin:
			items[i].UserCount = adminCount
		case PermissionGroupRegular:
			items[i].UserCount = regularCount
		default:
			var count int
			if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM user_permission_groups WHERE group_id=?`, items[i].ID).Scan(&count); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to count users")
				return
			}
			items[i].UserCount = count
		}
	}
	sortPermissionGroups(items)
	respondJSON(w, http.StatusOK, map[string]any{"items": items, "catalog": permissionCatalog()})
}

func (a *App) handleCreatePermissionGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Permissions []string          `json:"permissions"`
		Limits      *PermissionLimits `json:"limits"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		badRequest(w, errors.New("name is required"))
		return
	}
	permissions, err := normalizePermissionList(req.Permissions)
	if err != nil {
		badRequest(w, err)
		return
	}
	if !actorCanGrantPermissions(currentUser(r), permissions) {
		respondError(w, http.StatusForbidden, "cannot grant permissions you do not hold")
		return
	}
	limits := defaultPermissionLimits()
	if req.Limits != nil {
		var err error
		limits, err = normalizePermissionLimits(*req.Limits)
		if err != nil {
			badRequest(w, err)
			return
		}
	}
	if !actorCanGrantLimits(currentUser(r), limits) {
		respondError(w, http.StatusForbidden, "cannot grant limits above your own")
		return
	}
	id := newID("pg")
	now := a.now().UTC().Format(time.RFC3339Nano)
	systemColumn := permissionGroupSystemColumnSQL(databaseDriverSQLite)
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO permission_groups(id,name,description,permissions_json,limits_json,`+systemColumn+`,created_at,updated_at)
		VALUES(?,?,?,?,?,0,?,?)`, id, name, strings.TrimSpace(req.Description), encodePermissions(permissions), encodePermissionLimits(limits), now, now); err != nil {
		badRequest(w, err)
		return
	}
	group, err := a.permissionGroupByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load permission group")
		return
	}
	respondJSON(w, http.StatusCreated, group)
}

func (a *App) handleUpdatePermissionGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var existingSystem int
	systemColumn := permissionGroupSystemColumnSQL(databaseDriverSQLite)
	if err := a.db.QueryRowContext(r.Context(), `SELECT `+systemColumn+` FROM permission_groups WHERE id=?`, id).Scan(&existingSystem); err != nil {
		respondError(w, http.StatusNotFound, "permission group not found")
		return
	}
	if id == PermissionGroupSuperAdmin {
		respondError(w, http.StatusForbidden, "super administrator group cannot be edited")
		return
	}
	// 系统分组（含默认用户组）只有超级管理员能改：委派管理员改写它等于
	// 一次性给全站用户加权限，或把全站用户的权限清空。
	actor := currentUser(r)
	if intBool(existingSystem) && (actor == nil || actor.Role != "admin") {
		respondError(w, http.StatusForbidden, "system permission groups can only be edited by an administrator")
		return
	}
	// 非管理员只能改自己所在的分组，不能顺手把别的团队清空。
	if actor != nil && actor.Role != "admin" {
		var member int
		if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM user_permission_groups WHERE group_id=? AND user_id=?`, id, actor.ID).Scan(&member); err != nil || member == 0 {
			respondError(w, http.StatusForbidden, "cannot edit a permission group you do not belong to")
			return
		}
	}
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Permissions []string          `json:"permissions"`
		Limits      *PermissionLimits `json:"limits"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		badRequest(w, errors.New("name is required"))
		return
	}
	permissions, err := normalizePermissionList(req.Permissions)
	if err != nil {
		badRequest(w, err)
		return
	}
	if !actorCanGrantPermissions(currentUser(r), permissions) {
		respondError(w, http.StatusForbidden, "cannot grant permissions you do not hold")
		return
	}
	limits := defaultPermissionLimits()
	if req.Limits != nil {
		var err error
		limits, err = normalizePermissionLimits(*req.Limits)
		if err != nil {
			badRequest(w, err)
			return
		}
	} else {
		var rawLimits string
		if err := a.db.QueryRowContext(r.Context(), `SELECT limits_json FROM permission_groups WHERE id=?`, id).Scan(&rawLimits); err != nil {
			respondError(w, http.StatusNotFound, "permission group not found")
			return
		}
		limits = decodeStoredLimits(rawLimits)
	}
	if !actorCanGrantLimits(currentUser(r), limits) {
		respondError(w, http.StatusForbidden, "cannot grant limits above your own")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE permission_groups SET name=?,description=?,permissions_json=?,limits_json=?,updated_at=? WHERE id=?`,
		name, strings.TrimSpace(req.Description), encodePermissions(permissions), encodePermissionLimits(limits), a.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		badRequest(w, err)
		return
	}
	group, err := a.permissionGroupByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load permission group")
		return
	}
	respondJSON(w, http.StatusOK, group)
}

func (a *App) handleDeletePermissionGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var system int
	systemColumn := permissionGroupSystemColumnSQL(databaseDriverSQLite)
	if err := a.db.QueryRowContext(r.Context(), `SELECT `+systemColumn+` FROM permission_groups WHERE id=?`, id).Scan(&system); err != nil {
		respondError(w, http.StatusNotFound, "permission group not found")
		return
	}
	if intBool(system) {
		respondError(w, http.StatusForbidden, "system permission groups cannot be deleted")
		return
	}
	var userCount int
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM user_permission_groups WHERE group_id=?`, id).Scan(&userCount); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to check permission group")
		return
	}
	if userCount > 0 {
		badRequest(w, errors.New("permission group is assigned to users"))
		return
	}
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM permission_groups WHERE id=?`, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete permission group")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		respondError(w, http.StatusNotFound, "permission group not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

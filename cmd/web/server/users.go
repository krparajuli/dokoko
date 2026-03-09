package server

import (
	"net/http"

	authpkg "dokoko.ai/dokoko/internal/auth"
)

// listUsers handles GET /api/users (admin only).
// Returns all users with passwords omitted.
func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	users := h.authStore.ListUsers()
	type userResp struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	out := make([]userResp, len(users))
	for i, u := range users {
		out[i] = userResp{Username: u.Username, Role: string(u.Role)}
	}
	jsonOK(w, out)
}

// createUser handles POST /api/users (admin only).
// Body: {"username":"...","password":"...","role":"user"|"admin"}
func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decode(r, &body); err != nil || body.Username == "" || body.Password == "" {
		jsonErr(w, http.StatusBadRequest, "username and password are required")
		return
	}
	role := authpkg.RoleUser
	if body.Role == "admin" {
		role = authpkg.RoleAdmin
	}
	if err := h.authStore.CreateUser(authpkg.User{
		Username: body.Username,
		Password: body.Password,
		Role:     role,
	}); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	jsonOK(w, map[string]string{"username": body.Username, "role": string(role)})
}

// deleteUser handles DELETE /api/users/{username} (admin only).
func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	sess, _ := sessionFromContext(r.Context())
	username := r.PathValue("username")
	if username == sess.Username {
		jsonErr(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := h.authStore.DeleteUser(username); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]string{"message": "user deleted"})
}

// updateUserPassword handles PUT /api/users/{username}/password (admin only).
// Body: {"password":"..."}
func (h *handler) updateUserPassword(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	username := r.PathValue("username")
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil || body.Password == "" {
		jsonErr(w, http.StatusBadRequest, "password is required")
		return
	}
	if err := h.authStore.UpdatePassword(username, body.Password); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]string{"message": "password updated"})
}

// requireAdmin writes a 403 and returns false if the caller is not an admin.
func (h *handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := sessionFromContext(r.Context())
	if !ok || sess.Role != authpkg.RoleAdmin {
		jsonErr(w, http.StatusForbidden, "admin only")
		return false
	}
	return true
}

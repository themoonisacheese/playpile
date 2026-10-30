package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"playpile/internal/store"
)

// handleMembers shows and manages a crew's people and invites.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	crew, mem, _, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	people, err := s.st.Members(crew.ID)
	if err != nil {
		s.log.Error("list members", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load the members.")
		return
	}

	view := &membersPageView{
		baseView:  s.base(r),
		Crew:      crew,
		CrewID:    crew.ID,
		Role:      mem.Role,
		IsAdmin:   mem.Can(store.RoleAdmin),
		IsOwner:   mem.Role == store.RoleOwner,
		BaseURL:   s.baseURL(r),
		NewInvite: r.URL.Query().Get("new"),
	}

	adminCount := 0
	for _, p := range people {
		if p.Role == store.RoleOwner || p.Role == store.RoleAdmin {
			adminCount++
		}
	}
	for _, p := range people {
		mv := memberVM{
			ID:      p.ID,
			Name:    p.DisplayName,
			Avatar:  p.AvatarURL,
			Profile: p.ProfileURL,
			Role:    p.Role,
			IsMe:    p.ID == user.ID,
			Joined:  time.Unix(p.JoinedAt, 0).Format("2 Jan 2006"),
		}
		switch {
		case p.Role == store.RoleOwner:
			mv.CanEdit = false
			mv.CanRemove = false
		case mv.IsMe:
			// Stepping down is allowed as long as somebody else can run things.
			mv.CanEdit = view.IsAdmin && adminCount > 1
			mv.CanRemove = false
		default:
			mv.CanEdit = view.IsAdmin && store.Rank(p.Role) < store.Rank(mem.Role)
			mv.CanRemove = view.IsAdmin || (mv.IsMe && adminCount > 1)
		}
		view.Members = append(view.Members, mv)
	}

	if view.IsAdmin {
		invites, err := s.st.InvitesForCrew(crew.ID)
		if err != nil {
			s.log.Error("list invites", "err", err)
		}
		now := time.Now().Unix()
		for _, inv := range invites {
			iv := inviteVM{
				Token:   inv.Token,
				URL:     fmt.Sprintf("%s/join/%s", view.BaseURL, url.PathEscape(inv.Token)),
				Role:    inv.Role,
				Created: time.Unix(inv.CreatedAt, 0).Format("2 Jan 2006"),
				Revoked: inv.RevokedAt != 0,
				Usable:  inv.Usable(now),
			}
			switch {
			case inv.MaxUses > 0:
				iv.Uses = fmt.Sprintf("%d of %d", inv.Uses, inv.MaxUses)
			default:
				iv.Uses = fmt.Sprintf("%d", inv.Uses)
			}
			switch {
			case inv.RevokedAt != 0:
				iv.Expires = "revoked"
			case inv.ExpiresAt == 0:
				iv.Expires = "never"
			default:
				iv.Expires = time.Unix(inv.ExpiresAt, 0).Format("2 Jan 2006")
			}
			view.Invites = append(view.Invites, iv)
		}
	}

	s.render(w, r, "members", http.StatusOK, view)
}

// handleMemberRole changes somebody's role.
func (s *Server) handleMemberRole(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	back := fmt.Sprintf("/c/%d/members", crew.ID)
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if !mem.Can(store.RoleAdmin) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can change roles.")
		return
	}
	targetID, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "Unknown member.")
		return
	}
	target, err := s.st.Membership(crew.ID, targetID)
	if err != nil {
		s.notFound(w, r, "Unknown member.")
		return
	}
	role := r.PostFormValue("role")
	if target.Role == store.RoleOwner {
		s.back(w, r, back, "The owner's role cannot be changed.", "error")
		return
	}
	// Nobody can grant a role above their own, and nobody can promote
	// themselves to owner.
	if store.Rank(role) > store.Rank(mem.Role) || store.Rank(role) < store.Rank(store.RoleMember) || role == store.RoleOwner {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "That role is not yours to give.")
		return
	}
	if targetID == user.ID && store.Rank(role) < store.Rank(mem.Role) {
		admins, err := countAdmins(s, crew.ID)
		if err != nil || admins <= 1 {
			s.back(w, r, back, "You are the last admin here; promote somebody else first.", "error")
			return
		}
	}
	if err := s.st.SetMemberRole(crew.ID, targetID, role); err != nil {
		s.log.Error("set role", "err", err)
		s.back(w, r, back, "Could not change that role.", "error")
		return
	}
	s.back(w, r, back, "Role updated.", "ok")
}

// handleRemoveMember drops somebody from the crew.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	back := fmt.Sprintf("/c/%d/members", crew.ID)
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	targetID, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "Unknown member.")
		return
	}
	target, err := s.st.Membership(crew.ID, targetID)
	if err != nil {
		s.notFound(w, r, "Unknown member.")
		return
	}
	if target.Role == store.RoleOwner {
		s.back(w, r, back, "The owner cannot be removed.", "error")
		return
	}
	leaving := targetID == user.ID
	if !leaving {
		if !mem.Can(store.RoleAdmin) {
			s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can remove people.")
			return
		}
		if store.Rank(target.Role) >= store.Rank(mem.Role) {
			s.fail(w, r, http.StatusForbidden, "Forbidden", "You cannot remove somebody at your own level.")
			return
		}
	}
	if err := s.st.RemoveMember(crew.ID, targetID); err != nil {
		s.log.Error("remove member", "err", err)
		s.back(w, r, back, "Could not remove that member.", "error")
		return
	}
	if leaving {
		s.redirect(w, r, "/", "You left the crew.", "ok")
		return
	}
	s.back(w, r, back, "Member removed.", "ok")
}

func countAdmins(s *Server, crewID int64) (int, error) {
	people, err := s.st.Members(crewID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range people {
		if p.Role == store.RoleOwner || p.Role == store.RoleAdmin {
			n++
		}
	}
	return n, nil
}

// handleJoinPage shows the invite landing page.
func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := s.st.InviteByToken(token)
	if err != nil || !inv.Usable(time.Now().Unix()) {
		s.notFound(w, r, "That invite link is not valid any more.")
		return
	}
	crew, err := s.st.CrewByID(inv.CrewID)
	if err != nil {
		s.notFound(w, r, "That crew does not exist.")
		return
	}
	count, _ := s.st.MemberCount(crew.ID)

	if _, user, ok := s.session(r); ok {
		if _, err := s.st.Membership(crew.ID, user.ID); err == nil {
			s.redirect(w, r, fmt.Sprintf("/c/%d", crew.ID), "You are already in this crew.", "ok")
			return
		}
	} else {
		s.redirect(w, r, "/login?next="+url.QueryEscape("/join/"+token), "", "")
		return
	}

	s.render(w, r, "join", http.StatusOK, joinView{
		baseView: s.base(r),
		Crew:     crew,
		Token:    token,
		Members:  count,
		Role:     inv.Role,
		InviteOK: true,
	})
}

// handleJoin redeems an invite link.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	sess, user, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/login?next="+url.QueryEscape("/join/"+r.PathValue("token")), "", "")
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	crewID, err := s.st.RedeemInvite(r.PathValue("token"), user.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "That invite link is not valid any more.")
			return
		}
		s.log.Error("redeem invite", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not join that crew.")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/c/%d", crewID), "You joined the crew.", "ok")
}

package doppler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"sync"
)

// Member is one principal's access to a project.
//
// Slug identifies the workplace user or service account, not the person: the
// name and email live on the workplace user record, which is why Member is
// resolved against a separate listing before it is shown.
type Member struct {
	Type                  string `json:"type"`
	Slug                  string `json:"slug"`
	Role                  Role   `json:"role"`
	AccessAllEnvironments bool   `json:"access_all_environments"`
	// Environments is the explicit environment list, and is null when
	// AccessAllEnvironments is set — the two together are the whole grant.
	Environments []string `json:"environments"`
}

// Role names a project role by its stable identifier.
type Role struct {
	Identifier string `json:"identifier"`
}

// WorkplaceUser is a person in the workplace.
type WorkplaceUser struct {
	ID     string `json:"id"`
	Access string `json:"access"`
	User   struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"user"`
}

// Group is a workplace group that can hold project access. Groups are a
// first-class member type alongside users: a project's access list is not
// explicable without them.
type Group struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ProjectRole is a role a project member can hold.
type ProjectRole struct {
	Name        string   `json:"name"`
	Identifier  string   `json:"identifier"`
	Permissions []string `json:"permissions"`
	IsCustom    bool     `json:"is_custom_role"`
}

// ProjectMembers lists who has access to a project.
func (c *Client) ProjectMembers(ctx context.Context, project, etag string) ([]Member, string, bool, error) {
	q := url.Values{"project": {project}, "per_page": {strconv.Itoa(perPage)}}
	res, err := c.get(ctx, "/v3/projects/project/members", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Members []Member `json:"members"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse members: %w", err)
	}
	return env.Members, res.ETag, false, nil
}

// WorkplaceUsers lists everyone in the workplace, so a member's slug can be
// shown as a name instead of a UUID.
//
// It is paged like the project listing but fetched as a whole: a member list
// is useless without every name it might reference, and the workplace is
// small enough that partial resolution would only produce holes.
func (c *Client) WorkplaceUsers(ctx context.Context, prev []Page[WorkplaceUser]) ([]Page[WorkplaceUser], error) {
	revalidated, next, err := revalidateUserPages(ctx, c, prev)
	if err != nil {
		return nil, err
	}
	out := revalidated
	if next == 0 {
		return out, nil
	}
	// The walk ends on an empty page, not a short one: this endpoint ignores
	// per_page and returns a fixed 20 per page, so treating a short page as
	// the end stops after the first one and silently loses everyone past it.
	// The symptom is a member list where half the people show as UUIDs.
	for page := next; page <= runawayPages; page++ {
		items, etag, _, err := c.workplaceUserPage(ctx, page, "")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		out = append(out, Page[WorkplaceUser]{ETag: etag, Items: items})
	}
	return out, nil
}

func revalidateUserPages(ctx context.Context, c *Client, prev []Page[WorkplaceUser]) ([]Page[WorkplaceUser], int, error) {
	if len(prev) == 0 {
		return nil, 1, nil
	}
	type result struct {
		page Page[WorkplaceUser]
		err  error
	}
	results := make([]result, len(prev))
	var wg sync.WaitGroup
	for i := range prev {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			items, etag, notModified, err := c.workplaceUserPage(ctx, i+1, prev[i].ETag)
			switch {
			case err != nil:
				results[i] = result{err: err}
			case notModified:
				results[i] = result{page: prev[i]}
			default:
				results[i] = result{page: Page[WorkplaceUser]{ETag: etag, Items: items}}
			}
		}(i)
	}
	wg.Wait()

	out := make([]Page[WorkplaceUser], 0, len(prev))
	for _, r := range results {
		if r.err != nil {
			return nil, 0, r.err
		}
		if len(r.page.Items) == 0 {
			// The listing shrank past this page; everything after it is gone.
			return out, 0, nil
		}
		out = append(out, r.page)
	}
	// A short page does not end this listing (see WorkplaceUsers), so the walk
	// always continues past the cached pages until one comes back empty.
	return out, len(out) + 1, nil
}

func (c *Client) workplaceUserPage(ctx context.Context, page int, etag string) ([]WorkplaceUser, string, bool, error) {
	q := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	res, err := c.get(ctx, "/v3/workplace/users", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Users []WorkplaceUser `json:"workplace_users"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse workplace users: %w", err)
	}
	return env.Users, res.ETag, false, nil
}

// WorkplaceGroups lists the workplace's groups, so a group member resolves to
// a team name rather than a UUID.
func (c *Client) WorkplaceGroups(ctx context.Context, etag string) ([]Group, string, bool, error) {
	q := url.Values{"per_page": {strconv.Itoa(perPage)}}
	res, err := c.get(ctx, "/v3/workplace/groups", q, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Groups []Group `json:"groups"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse groups: %w", err)
	}
	return env.Groups, res.ETag, false, nil
}

// ProjectRoles lists the roles a project member can hold, so the role picker
// offers what the workplace actually defines rather than a hardcoded set that
// would miss custom roles.
func (c *Client) ProjectRoles(ctx context.Context, etag string) ([]ProjectRole, string, bool, error) {
	res, err := c.get(ctx, "/v3/projects/roles", nil, etag)
	if err != nil {
		return nil, "", false, err
	}
	if res.NotModified {
		return nil, res.ETag, true, nil
	}
	var env struct {
		Roles []ProjectRole `json:"roles"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, "", false, fmt.Errorf("parse project roles: %w", err)
	}
	return env.Roles, res.ETag, false, nil
}

// memberPath is the per-member endpoint, shared by the read, update and
// delete calls.
func memberPath(kind, slug string) string {
	return "/v3/projects/project/members/member/" + url.PathEscape(kind) + "/" + url.PathEscape(slug)
}

// SetMemberRole changes one member's project role.
func (c *Client) SetMemberRole(ctx context.Context, project string, m Member, role string) error {
	q := url.Values{"project": {project}}
	body := map[string]any{
		"role": map[string]string{"identifier": role},
		// The environment grant is part of the same payload; sending the
		// member's current grant back keeps a role change from silently
		// widening or narrowing which environments they can reach.
		"access_all_environments": m.AccessAllEnvironments,
	}
	if !m.AccessAllEnvironments {
		envs := m.Environments
		if envs == nil {
			envs = []string{}
		}
		body["environments"] = envs
	}
	return c.send(ctx, "PATCH", memberPath(m.Type, m.Slug), q, body)
}

// RemoveMember revokes a member's access to a project.
func (c *Client) RemoveMember(ctx context.Context, project string, m Member) error {
	return c.send(ctx, "DELETE", memberPath(m.Type, m.Slug), url.Values{"project": {project}}, nil)
}

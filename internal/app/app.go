// Package app wires config, the Doppler client and the cache into the single
// context that both the CLI and the TUI operate on.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/keyolk/dpx/internal/cache"
	"github.com/keyolk/dpx/internal/config"
	"github.com/keyolk/dpx/internal/doppler"
)

// DefaultTTL is how long a cached listing is used without revalidation.
//
// It is short because revalidation is cheap: an ETag sweep over an unchanged
// listing transfers no bodies, so there is little reason to trust stale data
// for long. What the TTL actually prevents is a revalidation storm when
// several dpx invocations run back to back — a shell completion loop, say.
const DefaultTTL = 5 * time.Minute

// ErrNoCache is returned in cache-only mode when no snapshot exists.
var ErrNoCache = errors.New("no cached snapshot")

// Context bundles everything a command needs.
type Context struct {
	Cfg    config.Config
	Client *doppler.Client
	Store  *cache.Store
	Path   string
	// Stale reports that the cached listing is past its TTL and the caller
	// should revalidate — asynchronously, in the TUI's case.
	Stale bool
}

// Options control how the context is built.
type Options struct {
	ConfigPath string
	// Refresh forces a revalidation before returning.
	Refresh bool
	// TTL overrides DefaultTTL.
	TTL time.Duration
	// AllowStale returns an expired snapshot instead of blocking to refresh it.
	AllowStale bool
	// DeferFetch returns an empty snapshot when no cache exists, leaving the
	// first fetch to the caller.
	DeferFetch bool
	// CacheOnly prohibits network access and returns ErrNoCache when absent.
	CacheOnly bool
	// Quiet suppresses progress output on stderr.
	Quiet bool
}

// Open loads config and returns a ready context.
//
// The behaviors are explicit rather than inferred: plain CLI commands refresh
// synchronously when the cache is cold or expired; the TUI passes
// AllowStale+DeferFetch so it can paint immediately and revalidate in a Bubble
// Tea command; shell completion passes CacheOnly and never touches the network.
func Open(ctx context.Context, opts Options) (*Context, error) {
	wd, err := os.Getwd()
	if err != nil {
		wd = "/"
	}
	cfg, err := config.Load(opts.ConfigPath, wd)
	if err != nil {
		return nil, err
	}
	client := doppler.New(cfg.APIHost, cfg.Token)
	path := cache.Path(cfg.CacheKey())

	ttl := opts.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}

	// The cache is always loaded, even for --refresh. Refresh means "do not
	// trust the TTL", not "throw the ETags away" — discarding them would turn
	// the cheapest possible refresh into a full re-download, which is the
	// opposite of what the flag is for. `dpx cache clear` is how you get a
	// genuine cold start.
	snap, err := cache.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load cache: %w", err)
	}

	if snap != nil {
		c := &Context{
			Cfg: cfg, Client: client, Path: path,
			Store: cache.NewStore(path, snap),
			Stale: snap.Age() > ttl,
		}
		if opts.CacheOnly || (!opts.Refresh && (!c.Stale || opts.AllowStale)) {
			return c, nil
		}
		if err := c.RefreshProjects(ctx); err != nil {
			return nil, err
		}
		return c, nil
	}

	if opts.CacheOnly {
		return nil, ErrNoCache
	}

	// Cold start. The workplace call both names the cache and proves the token
	// works — worth one request before anything else, since every later error
	// would otherwise be indistinguishable from a bad token.
	id, name, err := client.Workplace(ctx)
	if err != nil {
		return nil, describeAuth(err, cfg)
	}
	c := &Context{
		Cfg: cfg, Client: client, Path: path,
		Store: cache.NewStore(path, cache.Empty(id, name)),
		Stale: true,
	}
	if opts.DeferFetch {
		return c, nil
	}
	if err := c.RefreshProjects(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// RefreshProjects revalidates the project listing and persists the result.
func (c *Context) RefreshProjects(ctx context.Context) error {
	pages, err := c.Client.Projects(ctx, c.Store.ProjectPages())
	if err != nil {
		return describeAuth(err, c.Cfg)
	}
	c.Store.SetProjects(pages)
	c.Stale = false
	c.persist()
	return nil
}

// LoadProject revalidates one project's environments and configs.
//
// Both listings are conditional on the entry's stored ETags, so re-entering a
// project that has not changed costs two 304s and no transfer at all.
func (c *Context) LoadProject(ctx context.Context, project string) error {
	e := c.Store.Entry(project)
	var envETag, cfgETag string
	if e != nil {
		envETag, cfgETag = e.EnvETag, e.CfgETag
	}

	envs, newEnvETag, envUnchanged, err := c.Client.Environments(ctx, project, envETag)
	if err != nil {
		return err
	}
	cfgs, newCfgETag, cfgUnchanged, err := c.Client.Configs(ctx, project, cfgETag)
	if err != nil {
		return err
	}
	if envUnchanged {
		envs = nil
	}
	if cfgUnchanged {
		cfgs = nil
	}
	c.Store.SetProjectDetail(project, envs, newEnvETag, cfgs, newCfgETag)
	c.persist()
	return nil
}

// LoadSecretNames revalidates one config's secret names.
func (c *Context) LoadSecretNames(ctx context.Context, project, config string) error {
	var etag string
	if sn := c.Store.SecretNames(project, config); sn != nil {
		etag = sn.ETag
	}
	names, newETag, unchanged, err := c.Client.SecretNames(ctx, project, config, etag)
	if err != nil {
		return err
	}
	if unchanged {
		names = nil
	}
	c.Store.SetSecretNames(project, config, names, newETag)
	c.persist()
	return nil
}

// LoadMembers revalidates a project's access list, together with the
// workplace users and project roles it is rendered against.
//
// They are fetched as one operation because a member list alone is a page of
// UUIDs: names come from the user and group listings and the role labels from
// the role listing, and a screen missing any of them is not worth painting.
// All are conditional, so a repeat visit costs a handful of 304s.
func (c *Context) LoadMembers(ctx context.Context, project string) error {
	members, etag, unchanged, err := c.Client.ProjectMembers(ctx, project, c.Store.MemberETag(project))
	if err != nil {
		return err
	}
	if unchanged {
		members = nil
	}
	c.Store.SetMembers(project, members, etag)

	// The user listing is workplace-wide and shared by every project, so it is
	// only walked when it has never been fetched; a stale name is corrected by
	// an explicit refresh rather than on every project visit.
	if len(c.Store.UserPages()) == 0 {
		pages, err := c.Client.WorkplaceUsers(ctx, nil)
		if err != nil {
			return err
		}
		c.Store.SetUsers(pages)
	}

	groups, groupETag, groupUnchanged, err := c.Client.WorkplaceGroups(ctx, c.Store.GroupETag())
	if err != nil {
		return err
	}
	if groupUnchanged {
		groups = nil
	}
	c.Store.SetGroups(groups, groupETag)

	roles, roleETag, roleUnchanged, err := c.Client.ProjectRoles(ctx, c.Store.RoleETag())
	if err != nil {
		return err
	}
	if roleUnchanged {
		roles = nil
	}
	c.Store.SetRoles(roles, roleETag)

	c.persist()
	return nil
}

// SetMemberRole changes a member's project role and drops the cached list, so
// the refetch that follows shows what the API actually stored rather than
// what dpx assumed it would store.
func (c *Context) SetMemberRole(ctx context.Context, project string, m doppler.Member, role string) error {
	if err := c.Client.SetMemberRole(ctx, project, m, role); err != nil {
		return err
	}
	c.Store.InvalidateMembers(project)
	c.persist()
	return nil
}

// RemoveMember revokes a member's access to a project.
func (c *Context) RemoveMember(ctx context.Context, project string, m doppler.Member) error {
	if err := c.Client.RemoveMember(ctx, project, m); err != nil {
		return err
	}
	c.Store.InvalidateMembers(project)
	c.persist()
	return nil
}

// RevealSecrets fetches values for one config. The result is deliberately not
// cached: a values file on disk is a credential store, and dpx is a browser.
func (c *Context) RevealSecrets(ctx context.Context, project, config string) ([]doppler.Secret, error) {
	return c.Client.Secrets(ctx, project, config)
}

// Invalidate drops the on-disk cache.
func (c *Context) Invalidate() error { return cache.Clear(c.Path) }

// persist writes the cache, reporting a failure on stderr rather than failing
// the operation — a cache that cannot be written costs a future refresh, never
// correctness.
func (c *Context) persist() {
	if err := c.Store.Persist(); err != nil {
		fmt.Fprintf(os.Stderr, "dpx: warning: could not write cache: %v\n", err)
	}
}

// describeAuth turns a rejected token into an actionable message naming where
// the token came from, since a workplace with several service tokens makes
// "401 Unauthorized" alone unactionable.
func describeAuth(err error, cfg config.Config) error {
	var apiErr *doppler.APIError
	if errors.As(err, &apiErr) && apiErr.Unauthorized() {
		return fmt.Errorf("%w\ntoken source: %s — run `doppler login` or set $DOPPLER_TOKEN", err, cfg.Source)
	}
	return err
}

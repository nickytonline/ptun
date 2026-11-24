package broker

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	configpb "github.com/pomerium/pomerium/pkg/grpc/config"
)

// ErrNotManaged is returned when something tries to modify a route or policy
// the broker does not own (name prefix AND description marker are both required).
var ErrNotManaged = errors.New("object is not managed by tunnel-broker")

// routeClient is the subset of configconnect.ConfigServiceClient the broker
// uses. Tests provide a fake.
type routeClient interface {
	CreateRoute(context.Context, *connect.Request[configpb.CreateRouteRequest]) (*connect.Response[configpb.CreateRouteResponse], error)
	DeleteRoute(context.Context, *connect.Request[configpb.DeleteRouteRequest]) (*connect.Response[configpb.DeleteRouteResponse], error)
	GetRoute(context.Context, *connect.Request[configpb.GetRouteRequest]) (*connect.Response[configpb.GetRouteResponse], error)
	ListRoutes(context.Context, *connect.Request[configpb.ListRoutesRequest]) (*connect.Response[configpb.ListRoutesResponse], error)
	CreatePolicy(context.Context, *connect.Request[configpb.CreatePolicyRequest]) (*connect.Response[configpb.CreatePolicyResponse], error)
	DeletePolicy(context.Context, *connect.Request[configpb.DeletePolicyRequest]) (*connect.Response[configpb.DeletePolicyResponse], error)
	GetPolicy(context.Context, *connect.Request[configpb.GetPolicyRequest]) (*connect.Response[configpb.GetPolicyResponse], error)
	ListPolicies(context.Context, *connect.Request[configpb.ListPoliciesRequest]) (*connect.Response[configpb.ListPoliciesResponse], error)
}

// RouteStore creates and deletes broker-owned routes.
type RouteStore interface {
	// Create creates a policy (source PPL) and then a route that references it.
	// The route's name is also the policy's name. If the route fails, the policy is deleted.
	Create(ctx context.Context, route *configpb.Route, ppl []byte) (routeID, policyID string, err error)
	// Delete removes the route, then its policy (either id may be empty or already gone).
	Delete(ctx context.Context, routeID, policyID string) error
	ListManaged(ctx context.Context) ([]*configpb.Route, error)
	// DeleteOrphanPolicies deletes ephemeral:* policies for which keep returns false.
	DeleteOrphanPolicies(ctx context.Context, keep func(*configpb.Policy) bool) (int, error)
}

// Routes is the only code path that mutates Pomerium routes. Every mutation
// passes through guard, which enforces the ownership invariant: the name starts
// with the configured prefix AND the description carries ManagedMarker.
//
// Only the Raft leader serves the config API and followers reset the stream, so
// each call tries the endpoints in order, starting with the last that worked.
type Routes struct {
	clients []routeClient
	prefix  string
	last    atomic.Int32
}

// NewRoutes builds a Routes over the given clients (one per config API endpoint)
// using DefaultRoutePrefix.
func NewRoutes(clients []routeClient) *Routes {
	return NewRoutesWithPrefix(clients, DefaultRoutePrefix)
}

// NewRoutesWithPrefix is NewRoutes with a configured name prefix.
func NewRoutesWithPrefix(clients []routeClient, prefix string) *Routes {
	return &Routes{clients: clients, prefix: prefix}
}

// guard is the single ownership check used before every create, delete, adopt
// and orphan sweep. Callers pass the object as just (re-)read from the API.
func (r *Routes) guard(name, description string) error {
	if !Owned(r.prefix, name, description) {
		return fmt.Errorf("%w: %q", ErrNotManaged, name)
	}
	return nil
}

// try runs fn against each client, starting at the last good one.
func (r *Routes) try(ctx context.Context, fn func(routeClient) error) error {
	n := len(r.clients)
	if n == 0 {
		return errors.New("no config API endpoints configured")
	}
	start := int(r.last.Load())
	var errs []error
	for i := range n {
		idx := (start + i) % n
		err := fn(r.clients[idx])
		if err == nil {
			r.last.Store(int32(idx))
			return nil
		}
		// A definitive answer from the leader, or our own cancellation: stop.
		if connect.CodeOf(err) == connect.CodeNotFound || ctx.Err() != nil {
			r.last.Store(int32(idx))
			return err
		}
		errs = append(errs, fmt.Errorf("endpoint %d: %w", idx, err))
	}
	return errors.Join(errs...)
}

// Create creates the tunnel's policy and then its route. Core ignores
// Route.ppl_policies for routes stored through the config API; only
// Route.policy_ids (Policy objects, compiled from source_ppl) are enforced.
// The route name must carry the ephemeral: prefix.
func (r *Routes) Create(ctx context.Context, route *configpb.Route, ppl []byte) (routeID, policyID string, err error) {
	if err := r.guard(route.GetName(), route.GetDescription()); err != nil {
		return "", "", err
	}
	name, desc := route.GetName(), ManagedMarker
	err = r.try(ctx, func(c routeClient) error {
		res, err := c.CreatePolicy(ctx, connect.NewRequest(&configpb.CreatePolicyRequest{
			Policy: &configpb.Policy{Name: &name, Description: &desc, SourcePpl: proto.String(string(ppl))},
		}))
		if err != nil {
			return err
		}
		policyID = res.Msg.GetPolicy().GetId()
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("create policy: %w", err)
	}
	if policyID == "" {
		return "", "", errors.New("config API returned a policy without an id")
	}
	route.PolicyIds = []string{policyID}
	route.PplPolicies = nil
	err = r.try(ctx, func(c routeClient) error {
		res, err := c.CreateRoute(ctx, connect.NewRequest(&configpb.CreateRouteRequest{Route: route}))
		if err != nil {
			return err
		}
		routeID = res.Msg.GetRoute().GetId()
		return nil
	})
	if err == nil && routeID == "" {
		err = errors.New("config API returned a route without an id")
	}
	if err != nil {
		// Do not leave the policy behind.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = r.deletePolicy(cctx, policyID)
		return "", "", err
	}
	return routeID, policyID, nil
}

// Delete removes a route after re-reading it and checking its name, then its
// policy the same way. Anything already gone is not an error.
func (r *Routes) Delete(ctx context.Context, routeID, policyID string) error {
	if routeID == "" && policyID == "" {
		return errors.New("empty route and policy id")
	}
	if routeID != "" {
		err := r.try(ctx, func(c routeClient) error {
			got, err := c.GetRoute(ctx, connect.NewRequest(&configpb.GetRouteRequest{Id: routeID}))
			if err != nil {
				if connect.CodeOf(err) == connect.CodeNotFound {
					return nil
				}
				return err
			}
			if err := r.guard(got.Msg.GetRoute().GetName(), got.Msg.GetRoute().GetDescription()); err != nil {
				return err
			}
			_, err = c.DeleteRoute(ctx, connect.NewRequest(&configpb.DeleteRouteRequest{Id: routeID}))
			if connect.CodeOf(err) == connect.CodeNotFound {
				return nil
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	if policyID == "" {
		return nil
	}
	return r.deletePolicy(ctx, policyID)
}

// deletePolicy is the only policy deletion path; it checks the ephemeral: name.
func (r *Routes) deletePolicy(ctx context.Context, policyID string) error {
	return r.try(ctx, func(c routeClient) error {
		got, err := c.GetPolicy(ctx, connect.NewRequest(&configpb.GetPolicyRequest{Id: policyID}))
		if err != nil {
			if connect.CodeOf(err) == connect.CodeNotFound {
				return nil
			}
			return err
		}
		if err := r.guard(got.Msg.GetPolicy().GetName(), got.Msg.GetPolicy().GetDescription()); err != nil {
			return err
		}
		_, err = c.DeletePolicy(ctx, connect.NewRequest(&configpb.DeletePolicyRequest{Id: policyID}))
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil
		}
		return err
	})
}

// DeleteOrphanPolicies deletes ephemeral:* policies for which keep returns false.
func (r *Routes) DeleteOrphanPolicies(ctx context.Context, keep func(*configpb.Policy) bool) (int, error) {
	var pols []*configpb.Policy
	err := r.try(ctx, func(c routeClient) error {
		pols = pols[:0]
		for offset := uint64(0); ; {
			limit := uint64(200)
			res, err := c.ListPolicies(ctx, connect.NewRequest(&configpb.ListPoliciesRequest{Offset: &offset, Limit: &limit}))
			if err != nil {
				return err
			}
			pols = append(pols, res.Msg.GetPolicies()...)
			offset += uint64(len(res.Msg.GetPolicies()))
			if len(res.Msg.GetPolicies()) == 0 || offset >= res.Msg.GetTotalCount() {
				return nil
			}
		}
	})
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, p := range pols {
		if !Owned(r.prefix, p.GetName(), p.GetDescription()) || p.GetId() == "" || keep(p) {
			continue
		}
		if err := r.deletePolicy(ctx, p.GetId()); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// ListManaged returns all routes the broker owns (prefix and marker).
func (r *Routes) ListManaged(ctx context.Context) ([]*configpb.Route, error) {
	const page = 200
	var out []*configpb.Route
	err := r.try(ctx, func(c routeClient) error {
		out = out[:0]
		for offset := uint64(0); ; {
			limit := uint64(page)
			res, err := c.ListRoutes(ctx, connect.NewRequest(&configpb.ListRoutesRequest{Offset: &offset, Limit: &limit}))
			if err != nil {
				return err
			}
			for _, rt := range res.Msg.GetRoutes() {
				if Owned(r.prefix, rt.GetName(), rt.GetDescription()) {
					out = append(out, rt)
				}
			}
			offset += uint64(len(res.Msg.GetRoutes()))
			if len(res.Msg.GetRoutes()) == 0 || offset >= res.Msg.GetTotalCount() {
				return nil
			}
		}
	})
	return out, err
}

package sqlkit

import "context"

type (
	preferReplicaKey  struct{}
	forcePrimaryKey   struct{}
	requireReplicaKey struct{}
)

// PreferReplica marks ctx: read methods of Cluster may go to a replica (if one is usable and ctx has no tx).
func PreferReplica(ctx context.Context) context.Context {
	return context.WithValue(ctx, preferReplicaKey{}, true)
}

// ForcePrimary marks ctx: Cluster sends everything to primary. It overrides PreferReplica and RequireReplica.
func ForcePrimary(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcePrimaryKey{}, true)
}

// RequireReplica marks ctx: read methods of Cluster go to a usable replica only; if there is none
// they fail with ErrReplicaUnavailable and never touch primary (outside a transaction, which always uses
// its own connection). ForcePrimary overrides it; it takes precedence over PreferReplica.
func RequireReplica(ctx context.Context) context.Context {
	return context.WithValue(ctx, requireReplicaKey{}, true)
}

func forcedPrimary(ctx context.Context) bool {
	v, _ := ctx.Value(forcePrimaryKey{}).(bool)

	return v
}

// ReplicaPreferred reports whether ctx has PreferReplica and has no ForcePrimary.
func ReplicaPreferred(ctx context.Context) bool {
	if forcedPrimary(ctx) {
		return false
	}

	v, _ := ctx.Value(preferReplicaKey{}).(bool)

	return v
}

// ReplicaRequired reports whether ctx has RequireReplica and has no ForcePrimary.
func ReplicaRequired(ctx context.Context) bool {
	if forcedPrimary(ctx) {
		return false
	}

	v, _ := ctx.Value(requireReplicaKey{}).(bool)

	return v
}

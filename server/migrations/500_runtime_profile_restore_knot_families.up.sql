-- Restore `knot` and `knot-http` to the protocol-family whitelist after the
-- upstream merge.
--
-- This fork added the two Knot families in 282 / 283. Upstream independently
-- added `codearts`, `dsh`, `mcode`, `dim` and `zeroclaw` in 313-441, each time
-- by DROPping the CHECK and recreating it from a hardcoded list. Those lists
-- were written without knowledge of this fork, so 441 — the highest-numbered
-- migration touching the constraint, and therefore the one that wins — omits
-- both Knot families. The net effect on a merged database is that `knot` and
-- `knot-http` silently stop being insertable even though
-- RUNTIME_PROFILE_PROTOCOL_FAMILIES in packages/core/types/agent.ts still
-- offers them, so creating a Knot runtime profile fails on the CHECK.
--
-- The fix is a new trailing migration rather than an edit to 441: 441 may
-- already be recorded in schema_migrations on an upgraded database, where
-- editing it in place would never re-run.
--
-- NOT VALID matches every prior migration in this chain: it preserves
-- historical rows that predate the whitelist while enforcing the union for
-- new ones.
ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_protocol_family_check;

ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_protocol_family_check
    CHECK (protocol_family IN (
        'claude',
        'codebuddy',
        'codex',
        'copilot',
        'opencode',
        'codearts',
        'openclaw',
        'hermes',
        'pi',
        'cursor',
        'kimi',
        'reasonix',
        'dsh',
        'kiro',
        'antigravity',
        'qoder',
        'qoderclicn',
        'traecli',
        'deveco',
        'grok',
        'qwen',
        'qwenpaw',
        'mcode',
        'dim',
        'zeroclaw',
        'knot',
        'knot-http'
    )) NOT VALID;

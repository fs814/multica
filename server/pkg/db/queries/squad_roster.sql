-- name: ListSquadRosterAgents :many
-- Preserve the roster's agent kinds while loading only its leader and members.
SELECT a.* FROM agent a
WHERE a.workspace_id = $1
  AND (a.id = @leader_id OR a.id IN (
    SELECT member_id FROM squad_member
    WHERE squad_id = @squad_id AND member_type = 'agent'
  ));

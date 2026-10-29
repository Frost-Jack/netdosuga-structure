param([ValidateSet('sync','failover','rejoin')][string]$Action = 'sync')
$ErrorActionPreference = 'Stop'
$compose = Join-Path $PSScriptRoot '../compose.ha.yaml'
function Dc { & docker compose -f $compose @args; if ($LASTEXITCODE -ne 0) { throw "Docker compose failed: $args" } }
function Sql([string]$node,[string]$sql) { $result = Dc exec -T $node psql -U demo -d demo -At -v ON_ERROR_STOP=1 -c $sql; return ($result -join "`n").Trim() }
function WaitSql([string]$node,[string]$query,[string]$expected) {
  $end=(Get-Date).AddSeconds(90)
  do { try { if ((Sql $node $query) -eq $expected) { return } } catch { }; Start-Sleep -Seconds 1 } while ((Get-Date) -lt $end)
  throw "Timed out waiting for $node query: $query"
}
function AssertRoute([string]$node) {
  $end=(Get-Date).AddSeconds(40)
  do {
    try {
      $role=Dc exec -T $node sh /ha/check-primary.sh
      if(($role -join '').Trim() -eq 'f'){return}
    } catch {}
    Start-Sleep 1
  } while((Get-Date) -lt $end)
  throw 'Database router does not reach a writable primary'
}
# Route is a deliberately explicit operator decision, persisted across restarts.
$route=Join-Path $PSScriptRoot '../ha/postgres.cfg'
$config=Get-Content $route -Raw
$primary=if ($config -match 'pg-a:5432') {'pg-a'} else {'pg-b'}
$standby=if ($primary -eq 'pg-a') {'pg-b'} else {'pg-a'}
if ($Action -eq 'failover') {
  if ((Sql $standby 'select pg_is_in_recovery()') -ne 't') { throw 'Candidate is not a standby' }
  Dc stop -t 10 $primary
  $id=Dc ps -aq $primary
  $running=& docker inspect --format '{{.State.Running}}' $id
  if ($LASTEXITCODE -ne 0 -or $running -ne 'false') { throw 'Fencing cannot be proven; REFUSING promotion' }
  Sql $standby 'select pg_promote(true,60)' | Out-Null
  WaitSql $standby 'select pg_is_in_recovery()' 'f'
  Sql $standby "ALTER SYSTEM SET synchronous_standby_names = ''" | Out-Null
  Sql $standby 'select pg_reload_conf()' | Out-Null
  $config.Replace("$($primary):5432","$($standby):5432") | Set-Content $route -NoNewline
  Dc restart postgres
  AssertRoute $standby
  Write-Output "Fenced $primary; promoted $standby. DEGRADED: commits currently have one durable PostgreSQL copy. Run rejoin."
} elseif ($Action -eq 'rejoin') {
  Dc stop -t 10 $standby
  # A stopped former primary is never started directly: archive its PGDATA on its own HA volume,
  # then clone the authoritative primary. No volume deletion and no baseline database access.
  Dc run --rm --no-deps -e "RECLONE_FROM=$primary" -e RECLONE_ONLY=true $standby
  Dc up -d --no-build --no-deps --wait $standby
  Dc restart postgres
  & $PSCommandPath sync
  AssertRoute $primary
  if ($LASTEXITCODE -ne 0) { throw "Sync restoration failed" }
} else {
  WaitSql $primary "select count(*) from pg_stat_replication where application_name='$standby' and state='streaming'" '1'
  Sql $primary "ALTER SYSTEM SET synchronous_standby_names = '*'" | Out-Null
  Sql $primary 'select pg_reload_conf()' | Out-Null
  WaitSql $primary "select count(*) from pg_stat_replication where application_name='$standby' and sync_state='sync'" '1'
  Write-Output "Synchronous remote_apply active: primary=$primary standby=$standby"
}

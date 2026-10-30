param([switch]$DatabaseOnly)
$ErrorActionPreference='Stop'
$compose=Join-Path $PSScriptRoot '../compose.ha.yaml'
$report=[ordered]@{started_at=(Get-Date).ToUniversalTime().ToString('o'); checks=@(); passed=$false}
function Dc { & docker compose -f $compose @args; if ($LASTEXITCODE -ne 0) { throw "Docker failed: $args" } }
function Sql($node,$sql) { return ((Dc exec -T $node psql -U demo -d demo -At -v ON_ERROR_STOP=1 -c $sql) -join "`n").Trim() }
function Record($name,$evidence) { $script:report.checks+=@{name=$name;passed=$true;evidence=$evidence}; Write-Output "PASS $name" }
function RequestCatalog {
  $end=(Get-Date).AddSeconds(45)
  do { try { $r=Invoke-WebRequest http://localhost:8080/api/v1/shop/catalog -UseBasicParsing -TimeoutSec 6; if ($r.StatusCode -eq 200) { return $r } } catch {}; Start-Sleep 1 } while ((Get-Date) -lt $end)
  throw 'Catalog did not recover'
}
$sessions=@{}
function Login($actor) {
  $email=@{'customer-two'='buyer2';'customer-demo'='buyer';'vendor-demo'='vendor';'vendor-two'='vendor2';'admin-demo'='admin'}[$actor]
  $password=if($env:MVP_SEED_PASSWORD){$env:MVP_SEED_PASSWORD}else{'demo12345'}
  $body=@{email="$email@netdosuga.local";password=$password}|ConvertTo-Json -Compress
  $session=New-Object Microsoft.PowerShell.Commands.WebRequestSession
  Invoke-WebRequest http://localhost:8080/api/v1/auth/login -UseBasicParsing -Method Post -ContentType application/json -Body $body -WebSession $session -TimeoutSec 15 | Out-Null
  $script:sessions[$actor]=$session
}
function WaitApplicationRecovery {
  $deadline=(Get-Date).AddSeconds(60)
  do {
    $ready=$true
    $writers=@{'order-a'=8081;'order-b'=8081;'identity-service'=8084;'payment-mock'=8085;'notification-service'=8082}
    foreach($service in $writers.Keys) {
      try { Dc exec -T $service wget -q -O /dev/null "http://127.0.0.1:$($writers[$service])/readyz" 2>$null | Out-Null }
      catch { $ready=$false }
    }
    if($ready){
      try { RequestCatalog | Out-Null; return } catch {}
    }
    Start-Sleep 1
  } while((Get-Date)-lt $deadline)
  throw 'Applications did not recover writable database connections after topology restart'
}
function Api($method,$path,$body,$actor='customer-two',$key='') {
  if(!$sessions.ContainsKey($actor)){Login $actor}
  $headers=@{}
  if($key){$headers['Idempotency-Key']=$key}
  $args=@{Uri="http://localhost:8080/api/v1/shop$path";Method=$method;Headers=$headers;WebSession=$sessions[$actor];UseBasicParsing=$true;TimeoutSec=15}
  if($null -ne $body){$args.Body=($body | ConvertTo-Json -Compress);$args.ContentType='application/json'}
  return ((Invoke-WebRequest @args).Content | ConvertFrom-Json)
}
$needsRejoin=$false
try {
  & (Join-Path $PSScriptRoot ha-postgres.ps1) sync
  $config=Get-Content (Join-Path $PSScriptRoot '../ha/postgres.cfg') -Raw
  $primary=if($config -match 'pg-a:5432'){'pg-a'}else{'pg-b'}
  $standby=if($primary -eq 'pg-a'){'pg-b'}else{'pg-a'}
  $marker=[guid]::NewGuid().ToString()
  Sql $primary "CREATE TABLE IF NOT EXISTS ha_probe (id text PRIMARY KEY); INSERT INTO ha_probe VALUES ('$marker');" | Out-Null
  if ((Sql $standby "SELECT id FROM ha_probe WHERE id='$marker' AND pg_is_in_recovery()") -ne $marker) { throw 'Committed marker missing from actual standby' }
  Record 'synchronous_streaming_replica_read' @{primary=$primary;standby=$standby;marker=$marker;replication=(Sql $primary 'select application_name,state,sync_state from pg_stat_replication')}
  if (!$DatabaseOnly) {
    $gateways=@{}; $orders=@{}
    1..24 | ForEach-Object { $r=RequestCatalog; $g=[string]@($r.Headers['X-Gateway-Instance'])[0]; $o=[string]@($r.Headers['X-Order-Instance'])[0]; $gateways[$g]=$true; $orders[$o]=$true }
    if($gateways.Count -ne 2 -or $orders.Count -ne 2){throw "Load distribution failed: gateways=$($gateways.Keys) orders=$($orders.Keys)"}
    Record 'two_gateway_two_order_distribution' @{gateways=@($gateways.Keys);orders=@($orders.Keys)}
    foreach($service in @('gateway-a','order-a')) {
      try { Dc stop $service; Start-Sleep 3; $r=RequestCatalog; Record "request_survives_$service" @{status=$r.StatusCode;gateway=$r.Headers['X-Gateway-Instance'];order=$r.Headers['X-Order-Instance']} }
      finally { Dc up -d --no-build --no-deps --wait $service }
    }
  }
  if (!$DatabaseOnly) {
    # Build a fresh offer before stopping Kafka, then commit checkout while the broker is absent.
    $app=(Api Post /applications @{organization='Kafka fault probe';title='Outbox recovery';city='Moscow';category='test'} 'vendor-two').application
    $offer=(Api Post "/applications/$($app.id)/approve" @{} 'admin-demo').offer
    Api Patch "/offers/$($offer.id)" @{title='Outbox recovery';description='Fault test';city='Moscow';category='test';price_cents=100;capacity=2} 'vendor-two' | Out-Null
    Api Post "/offers/$($offer.id)/publish" @{} 'vendor-two' | Out-Null
    foreach($item in (Api Get /cart $null).cart.items){Api Delete "/cart/items/$($item.offer_id)" $null | Out-Null}
    Api Post /cart/items @{offer_id=$offer.id;quantity=1} | Out-Null
    try {
      Dc stop kafka
      $order=(Api Post /orders @{} 'customer-two' ([guid]::NewGuid().ToString())).order
      $pending=Sql $primary "SELECT event_id FROM business_outbox WHERE payload->>'order_id'='$($order.id)' AND published_at IS NULL"
      if(!$pending){throw 'Checkout did not persist a pending outbox event during Kafka outage'}
      Record 'kafka_outage_checkout_durable_outbox' @{order_id=$order.id;event_id=$pending}
    } finally { Dc up -d --no-build --no-deps --wait kafka }
    $end=(Get-Date).AddSeconds(120)
    do {
      $delivered=Sql $primary "SELECT count(*) FROM business_notifications WHERE event_id='$pending'"
      if($delivered -eq '1'){break}; Start-Sleep 2
    } while((Get-Date) -lt $end)
    if($delivered -ne '1'){throw 'Outbox did not drain to notification after Kafka recovery'}
    if((Sql $primary "SELECT count(*) FROM business_outbox WHERE event_id='$pending' AND published_at IS NOT NULL") -ne '1'){throw 'Outbox publication marker missing'}
    Record 'kafka_restart_backlog_delivered_once' @{order_id=$order.id;event_id=$pending;notification_rows=$delivered}
    Api Post "/orders/$($order.id)/cancel" @{} | Out-Null
  }
  $needsRejoin=$true
  & (Join-Path $PSScriptRoot ha-postgres.ps1) failover
  if ((Sql $standby "SELECT id FROM ha_probe WHERE id='$marker' AND NOT pg_is_in_recovery()") -ne $marker) { throw 'Promoted primary lost acknowledged write' }
  Sql $standby "INSERT INTO ha_probe VALUES ('$marker-after');" | Out-Null
  Record 'fenced_manual_primary_failover' @{old_primary=$primary;new_primary=$standby;preserved_marker=$marker;degraded_write="$marker-after"}
  # Ordinary startup must not defeat fencing by reopening the former primary's PGDATA.
  Dc up -d --no-build --no-deps $primary
  $deadline=(Get-Date).AddSeconds(20)
  do {
    $formerID=Dc ps -aq $primary
    $running=(& docker inspect --format '{{.State.Running}}' $formerID)-join ''
    if($running.Trim() -eq 'false'){break}
    Start-Sleep 1
  } while((Get-Date)-lt $deadline)
  if($running.Trim() -ne 'false'){throw 'Fenced former primary became running after ordinary compose up'}
  $fenceLogs=(Dc logs --no-color --no-log-prefix $primary)-join "`n"
  if($fenceLogs -notmatch 'Refusing to start fenced former primary'){throw 'Former primary exited without proven persistent startup fence'}
  Record 'ordinary_start_cannot_bypass_persisted_primary_fence' @{node=$primary;running=$false}
  if (!$DatabaseOnly) {
    $r=RequestCatalog
    $write=Api Post /applications @{organization='HA probe';title='After failover';city='Moscow';category='test'} 'vendor-demo'
    Record 'application_write_after_failover' $write.application.id
  }
  & (Join-Path $PSScriptRoot ha-postgres.ps1) rejoin
  $needsRejoin=$false
  if ((Sql $primary "SELECT count(*) FROM ha_probe WHERE id IN ('$marker','$marker-after') AND pg_is_in_recovery()") -ne '2') {throw 'Rejoined standby lacks pre/post failover markers'}
  Record 'former_primary_recloned_sync_restored' (Sql $standby 'select application_name,state,sync_state from pg_stat_replication')
  Dc restart pg-a pg-b
  & (Join-Path $PSScriptRoot ha-postgres.ps1) sync
  $role=Dc exec -T $standby sh /ha/check-primary.sh
  if(($role -join '').Trim() -ne 'f'){throw 'Restarted router reaches a standby'}
  if(!$DatabaseOnly){
    WaitApplicationRecovery
    $postRestart=Api Post /applications @{organization='HA restart probe';title='After topology restart';city='Moscow';category='test'} 'vendor-demo'
    Record 'business_write_after_topology_restart' $postRestart.application.id
  }
  Record 'restart_preserves_promoted_topology'  @{primary=$standby;standby=$primary}
  $report.passed=$true
} catch { $report.failure=$_.Exception.Message; throw }
finally {
  if($needsRejoin) { try { & (Join-Path $PSScriptRoot ha-postgres.ps1) rejoin } catch { $report.recovery_failure=$_.Exception.Message } }
  if(!$DatabaseOnly) { try { Dc up -d --no-build --no-deps --wait gateway-a gateway-b order-a order-b kafka } catch { $report.container_recovery_failure=$_.Exception.Message } }
  if($report.recovery_failure -or $report.container_recovery_failure){$report.passed=$false}
  $report.finished_at=(Get-Date).ToUniversalTime().ToString('o')
  New-Item -ItemType Directory -Force (Join-Path $PSScriptRoot '../test-results') | Out-Null
  $out=Join-Path $PSScriptRoot '../test-results/ha-failures.json'
  $report | ConvertTo-Json -Depth 12 | Set-Content $out
  Write-Output "Evidence: $out"
  if($report.recovery_failure -or $report.container_recovery_failure){throw "HA test recovery failed; inspect $out"}
}

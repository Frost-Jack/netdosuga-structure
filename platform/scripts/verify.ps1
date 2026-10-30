param([switch]$HA)
$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
try {
    $composeFile=if($HA){'compose.ha.yaml'}else{'compose.yaml'}
    $database='postgres'
    $logServices=@('gateway','order-service','notification-service','integration-mock','identity-service','payment-mock')
    if($HA){
      $route=Get-Content ha/postgres.cfg -Raw
      $database=if($route -match 'pg-a:5432'){'pg-a'}else{'pg-b'}
      $logServices=@('gateway-a','gateway-b','order-a','order-b','notification-service','integration-mock','identity-service','payment-mock')
    }
    $env:STACK_NETWORK=if($HA){'netdosuga-ha_default'}else{'netdosuga-demo_default'}
    $env:EXPECTED_PROM_TARGETS=if($HA){'8'}else{'6'}
    New-Item -ItemType Directory -Force test-results | Out-Null
    docker compose -f compose.tests.yaml run --rm smoke
    if ($LASTEXITCODE -ne 0) { throw 'Network smoke failed; inspect test-results/smoke.json' }
    $report = Get-Content test-results/smoke.json -Raw | ConvertFrom-Json
    $ids = @($report.scenarios.success.body.event_id, $report.scenarios.delay.body.event_id)
    foreach ($id in $ids) { if ($id -notmatch '^[a-fA-F0-9-]{36}$') { throw 'Invalid event ID in smoke result' } }
    $sql = "SELECT event_id, scenario FROM demo_receipts WHERE event_id IN ('$($ids[0])','$($ids[1])') ORDER BY scenario;"
    $receipts = @(docker compose -f $composeFile exec -T $database psql -U demo -d demo -At -F ',' -c $sql)
    if ($LASTEXITCODE -ne 0 -or $receipts.Count -ne 2) { throw 'Expected two demo receipts' }
    $receipts | Set-Content test-results/receipts.csv
    $counts = @(docker compose -f $composeFile exec -T $database psql -U demo -d demo -At -c "SELECT count(*) FROM demo_receipts WHERE scenario NOT IN ('success','delay');")
    if ($LASTEXITCODE -ne 0 -or $counts[0] -ne '0') { throw 'Failed scenarios wrote a receipt' }
    $ErrorActionPreference = 'Continue' # Kafka consumer exits after its bounded read timeout.
    $events = @(docker compose -f $composeFile exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 --topic netdosuga.demo.orders.v1 --from-beginning --timeout-ms 5000 --max-messages 10000 2>$null)
    $ErrorActionPreference = 'Stop'
    $events | Set-Content test-results/kafka-events.jsonl -Encoding utf8
    docker compose -f compose.tests.yaml run --rm smoke python validate_events.py
    if ($LASTEXITCODE -ne 0) { throw 'Kafka contract validation failed' }
    $logs = @(docker compose -f $composeFile logs --no-color --no-log-prefix @logServices)
    $parsed = @($logs | ForEach-Object { try { $_ | ConvertFrom-Json -ErrorAction Stop } catch {} })
    $services = @('api-gateway','orders-service','notification-worker','mail-mock')
    $evidence = @()
    foreach ($service in $services) {
        $matches = @($parsed | Where-Object { $_.service -eq $service -and $_.trace_id -eq $report.scenarios.success.body.trace_id -and $_.span_id -and $_.timestamp -and $_.level -and $_.message })
        if ($matches.Count -eq 0) { throw "Missing correlated JSON log for $service" }
        $evidence += $matches[0]
    }
    $evidence | ConvertTo-Json -Depth 8 | Set-Content test-results/correlated-logs.json
    @{ passed = $true; receipts = $receipts; failed_scenario_receipts = 0; correlated_services = $services } | ConvertTo-Json -Depth 6 | Set-Content test-results/verification.json
    Write-Host 'Smoke, receipts, and correlated JSON logs passed. Reports: platform/test-results/'
} finally { Pop-Location }

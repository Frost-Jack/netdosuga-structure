$ErrorActionPreference='Stop'
$platform=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$project='netdosuga-bootstrap-'+[guid]::NewGuid().ToString('N').Substring(0,8)
$main=Join-Path $platform 'compose.ha.yaml'
$override=Join-Path $platform 'compose.bootstrap.yaml'
$emptyEnv=Join-Path $platform 'tests/bootstrap.env'
$tests=Join-Path $platform 'compose.tests.yaml'
foreach($path in @($main,$override,$emptyEnv,$tests)) {
  $resolved=[IO.Path]::GetFullPath($path)
  if(!$resolved.StartsWith($platform+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -or !(Test-Path -LiteralPath $resolved -PathType Leaf)) {
    throw "Bootstrap input is outside the platform workspace or missing: $resolved"
  }
}
if($project -notmatch '^netdosuga-bootstrap-[a-f0-9]{8}$'){throw 'Invalid disposable bootstrap project name'}
$cleanupAuthorized=$false
$bootstrapResult=[ordered]@{project=$project;started_at=(Get-Date).ToUniversalTime().ToString('o');passed=$false}
$results=[IO.Path]::GetFullPath((Join-Path $platform "test-results/$project"))
if(!$results.StartsWith($platform+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'Bootstrap reports must remain inside platform'}
New-Item -ItemType Directory -Force -Path $results | Out-Null
$oldResults=$env:TEST_RESULTS_DIR
$oldNetwork=$env:STACK_NETWORK
$oldTargets=$env:EXPECTED_PROM_TARGETS
$oldPgPassword=$env:POSTGRES_PASSWORD
$oldGrafanaPassword=$env:GRAFANA_ADMIN_PASSWORD
$oldSeedPassword=$env:MVP_SEED_PASSWORD
function Fresh { & docker compose --env-file $emptyEnv -p $project -f $main -f $override @args; if($LASTEXITCODE -ne 0){throw "Fresh bootstrap command failed: $args"} }
try {
  # A UUID is not sufficient ownership proof: refuse any pre-existing project resources.
  foreach($kind in @('container','volume','network')) {
    $existing=@(& docker $kind ls -q --filter "label=com.docker.compose.project=$project")
    if($LASTEXITCODE -ne 0){throw "Cannot inspect bootstrap $kind resources"}
    if($existing.Count -gt 0){throw 'Disposable bootstrap project unexpectedly already owns resources'}
  }
  $cleanupAuthorized=$true
  # Explicit empty environment values exercise Compose defaults, not saved credentials.
  $env:POSTGRES_PASSWORD='';$env:GRAFANA_ADMIN_PASSWORD='';$env:MVP_SEED_PASSWORD=''
  Fresh up -d --build --wait --wait-timeout 360
  $env:STACK_NETWORK="$($project)_default";$env:EXPECTED_PROM_TARGETS='8';$env:TEST_RESULTS_DIR=$results
  & docker compose --env-file $emptyEnv -p "$project-tests" -f $tests run --rm tests
  if($LASTEXITCODE -ne 0){throw 'Fresh bootstrap acceptance failed'}
  $bootstrapResult.passed=$true
  Write-Output "Fresh zero-config bootstrap and acceptance passed: $project; reports: $results"
} catch {
  $bootstrapFailure=$_
  $bootstrapResult.failure=$_.Exception.Message
  # Capture only sanitized application/Kafka logs and aggregate SQL state, never config/env.
  try {
    Fresh ps -a | Out-File (Join-Path $results 'containers.txt') -Encoding utf8
    Fresh logs --no-color --no-log-prefix --tail 500 order-a order-b notification-service integration-mock identity-service payment-mock kafka | Out-File (Join-Path $results 'application-kafka.log') -Encoding utf8
    Fresh exec -T pg-a psql -U demo -d demo -At -v ON_ERROR_STOP=1 -c "SELECT count(*) AS total,count(*) FILTER (WHERE published_at IS NULL) AS pending FROM business_outbox; SELECT count(*) AS notifications FROM business_notifications;" | Out-File (Join-Path $results 'outbox-notification-counts.txt') -Encoding utf8
  } catch {
    'Some bootstrap diagnostics could not be captured; inspect available files.' | Out-File (Join-Path $results 'diagnostics-incomplete.txt') -Encoding utf8
  }
  Write-Output "Fresh bootstrap failed; preserved reports: $results"
  throw $bootstrapFailure
} finally {
  $bootstrapResult.finished_at=(Get-Date).ToUniversalTime().ToString('o')
  $bootstrapResult | ConvertTo-Json -Depth 4 | Out-File (Join-Path $results 'bootstrap.json') -Encoding utf8
  # This UUID project owns only disposable bootstrap-test volumes.
  try {
    if($cleanupAuthorized) {
      # Verify each candidate volume belongs to this exact fresh project before deletion.
      $volumes=@(& docker volume ls -q --filter "label=com.docker.compose.project=$project")
      if($LASTEXITCODE -ne 0){throw 'Cannot verify disposable bootstrap volumes'}
      foreach($volume in $volumes) {
        $metadata=(& docker volume inspect $volume | ConvertFrom-Json)
        $inspectExit=$LASTEXITCODE
        $owner=[string]$metadata[0].Labels.'com.docker.compose.project'
        if($inspectExit -ne 0 -or $owner -ne $project -or !$volume.StartsWith($project+'_')) {
          throw "Refusing cleanup of unowned bootstrap volume: $volume"
        }
      }
      Fresh down --volumes --remove-orphans
    }
  } finally {
  $env:TEST_RESULTS_DIR=$oldResults
  $env:STACK_NETWORK=$oldNetwork;$env:EXPECTED_PROM_TARGETS=$oldTargets
  $env:POSTGRES_PASSWORD=$oldPgPassword;$env:GRAFANA_ADMIN_PASSWORD=$oldGrafanaPassword;$env:MVP_SEED_PASSWORD=$oldSeedPassword
  }
}

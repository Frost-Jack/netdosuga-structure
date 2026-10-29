$ErrorActionPreference = 'Stop'
$destination = Join-Path $PSScriptRoot '../.env'
if (Test-Path -LiteralPath $destination) { throw '.env already exists; preserve it or remove it explicitly before setup.' }
function New-LocalPassword {
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}
$content = "POSTGRES_PASSWORD=$(New-LocalPassword)`nGRAFANA_ADMIN_PASSWORD=$(New-LocalPassword)`n"
[IO.File]::WriteAllText([IO.Path]::GetFullPath($destination), $content, (New-Object Text.UTF8Encoding($false)))
Write-Host 'Created platform/.env with random local credentials. Values were not printed.'

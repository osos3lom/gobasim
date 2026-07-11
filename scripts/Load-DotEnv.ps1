# Loads KEY=VALUE lines from a .env file into the current PowerShell session.
Get-Content .env | Where-Object { $_ -match '^\s*[^#].*=' } | ForEach-Object {
    $name, $value = $_ -split '=', 2
    $name  = $name.Trim()
    # strip trailing " # inline comment" and surrounding whitespace
    $value = ($value -replace '\s+#.*$', '').Trim()
    [System.Environment]::SetEnvironmentVariable($name, $value, 'Process')
}
Write-Host "Loaded .env into this session."

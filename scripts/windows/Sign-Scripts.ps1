# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Spiral Pool - PowerShell Script Signing Infrastructure

.DESCRIPTION
    Creates a self-signed code signing certificate and signs all Spiral Pool
    PowerShell scripts for security validation.

    For production use, replace the self-signed certificate with a trusted
    Authenticode certificate from a Certificate Authority.

.NOTES
    Version: 1.1.1
    Author: Spiral Pool Contributors

    IMPORTANT: Self-signed certificates are for development/testing only.
    Production deployments should use certificates from trusted CAs.

.EXAMPLE
    # Create certificate and sign all scripts
    .\Sign-Scripts.ps1

    # Sign with existing certificate
    .\Sign-Scripts.ps1 -CertificateThumbprint "ABC123..."

    # Verify signatures only
    .\Sign-Scripts.ps1 -VerifyOnly
#>

param(
    [string]$CertificateThumbprint = "",

    [switch]$VerifyOnly,

    [string]$ScriptsPath = "",

    [switch]$CreateCertificate
)

$ErrorActionPreference = "Stop"

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

$CertName = "Spiral Pool Code Signing"
$CertSubject = "CN=Spiral Pool Code Signing, O=Spiral Pool, L=Local"

# Default to script directory's parent for scripts path
if ([string]::IsNullOrEmpty($ScriptsPath)) {
    $ScriptsPath = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}

# ═══════════════════════════════════════════════════════════════════════════════
# FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

function Write-Banner {
    Write-Host ""
    Write-Host "╔════════════════════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
    Write-Host "║          SPIRAL POOL - POWERSHELL SCRIPT SIGNING                           ║" -ForegroundColor Cyan
    Write-Host "╚════════════════════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
    Write-Host ""
}

function Get-OrCreateCodeSigningCert {
    param([string]$Thumbprint)

    # If thumbprint provided, try to find existing certificate
    if (-not [string]::IsNullOrEmpty($Thumbprint)) {
        $cert = Get-ChildItem -Path Cert:\CurrentUser\My -CodeSigningCert |
            Where-Object { $_.Thumbprint -eq $Thumbprint }

        if ($cert) {
            Write-Host "  [OK] Using existing certificate: $($cert.Subject)" -ForegroundColor Green
            return $cert
        } else {
            Write-Host "  [!] Certificate with thumbprint $Thumbprint not found" -ForegroundColor Yellow
        }
    }

    # Look for existing Spiral Pool certificate
    $existingCert = Get-ChildItem -Path Cert:\CurrentUser\My -CodeSigningCert |
        Where-Object { $_.Subject -match "Spiral Pool" } |
        Select-Object -First 1

    if ($existingCert -and -not $CreateCertificate) {
        Write-Host "  [OK] Found existing Spiral Pool certificate" -ForegroundColor Green
        Write-Host "       Subject: $($existingCert.Subject)" -ForegroundColor DarkGray
        Write-Host "       Thumbprint: $($existingCert.Thumbprint)" -ForegroundColor DarkGray
        Write-Host "       Expires: $($existingCert.NotAfter)" -ForegroundColor DarkGray
        return $existingCert
    }

    # Create new self-signed certificate
    Write-Host "  [*] Creating self-signed code signing certificate..." -ForegroundColor Cyan

    try {
        $cert = New-SelfSignedCertificate `
            -Subject $CertSubject `
            -Type CodeSigningCert `
            -KeyUsage DigitalSignature `
            -KeyAlgorithm RSA `
            -KeyLength 4096 `
            -HashAlgorithm SHA256 `
            -NotAfter (Get-Date).AddYears(5) `
            -CertStoreLocation Cert:\CurrentUser\My

        Write-Host "  [OK] Certificate created successfully" -ForegroundColor Green
        Write-Host "       Subject: $($cert.Subject)" -ForegroundColor DarkGray
        Write-Host "       Thumbprint: $($cert.Thumbprint)" -ForegroundColor DarkGray
        Write-Host "       Valid Until: $($cert.NotAfter)" -ForegroundColor DarkGray

        # Export certificate for distribution
        $exportPath = Join-Path $ScriptsPath "scripts\windows\SpiralPool-CodeSigning.cer"
        Export-Certificate -Cert $cert -FilePath $exportPath -Force | Out-Null
        Write-Host "  [OK] Public certificate exported to: $exportPath" -ForegroundColor Green
        Write-Host ""
        Write-Host "  IMPORTANT: This is a self-signed certificate for development/testing." -ForegroundColor Yellow
        Write-Host "  For production, obtain a certificate from a trusted Certificate Authority." -ForegroundColor Yellow
        Write-Host ""

        return $cert
    } catch {
        Write-Host "  [X] Failed to create certificate: $_" -ForegroundColor Red
        exit 1
    }
}

function Get-PowerShellScripts {
    param([string]$Path)

    $scripts = Get-ChildItem -Path $Path -Filter "*.ps1" -Recurse -File |
        Where-Object {
            # Exclude this signing script to prevent re-signing loop
            $_.Name -ne "Sign-Scripts.ps1"
        }

    return $scripts
}

function Sign-PowerShellScript {
    param(
        [System.Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [System.IO.FileInfo]$Script
    )

    try {
        $signature = Set-AuthenticodeSignature `
            -FilePath $Script.FullName `
            -Certificate $Certificate `
            -TimestampServer "http://timestamp.digicert.com" `
            -HashAlgorithm SHA256

        if ($signature.Status -eq "Valid") {
            return @{ Status = "Signed"; Message = "Successfully signed" }
        } else {
            return @{ Status = "Warning"; Message = $signature.StatusMessage }
        }
    } catch {
        return @{ Status = "Error"; Message = $_.Exception.Message }
    }
}

function Test-ScriptSignature {
    param([System.IO.FileInfo]$Script)

    try {
        $signature = Get-AuthenticodeSignature -FilePath $Script.FullName

        switch ($signature.Status) {
            "Valid" {
                return @{
                    Status = "Valid"
                    Signer = $signature.SignerCertificate.Subject
                    Timestamp = $signature.TimeStamperCertificate.Subject
                }
            }
            "NotSigned" {
                return @{ Status = "NotSigned"; Message = "Script is not signed" }
            }
            "HashMismatch" {
                return @{ Status = "Invalid"; Message = "Script has been modified" }
            }
            "NotTrusted" {
                return @{ Status = "NotTrusted"; Message = "Certificate not trusted" }
            }
            default {
                return @{ Status = "Unknown"; Message = $signature.StatusMessage }
            }
        }
    } catch {
        return @{ Status = "Error"; Message = $_.Exception.Message }
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN EXECUTION
# ═══════════════════════════════════════════════════════════════════════════════

Write-Banner

if ($VerifyOnly) {
    Write-Host "  Mode: Verification Only" -ForegroundColor Cyan
} else {
    Write-Host "  Mode: Sign Scripts" -ForegroundColor Cyan
}
Write-Host "  Scripts Path: $ScriptsPath" -ForegroundColor Cyan
Write-Host ""

# Get scripts
$scripts = Get-PowerShellScripts -Path $ScriptsPath
Write-Host "  Found $($scripts.Count) PowerShell scripts" -ForegroundColor White
Write-Host ""

if ($VerifyOnly) {
    # Verification mode
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    SIGNATURE VERIFICATION" -ForegroundColor White
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""

    $validCount = 0
    $notSignedCount = 0
    $invalidCount = 0

    foreach ($script in $scripts) {
        $result = Test-ScriptSignature -Script $script
        $relativePath = $script.FullName.Replace($ScriptsPath, "").TrimStart("\")

        switch ($result.Status) {
            "Valid" {
                Write-Host "    [OK] " -ForegroundColor Green -NoNewline
                Write-Host $relativePath -ForegroundColor White
                $validCount++
            }
            "NotSigned" {
                Write-Host "    [--] " -ForegroundColor DarkGray -NoNewline
                Write-Host "$relativePath - Not signed" -ForegroundColor DarkGray
                $notSignedCount++
            }
            "NotTrusted" {
                Write-Host "    [!] " -ForegroundColor Yellow -NoNewline
                Write-Host "$relativePath - $($result.Message)" -ForegroundColor Yellow
                $validCount++  # Still valid signature, just not trusted
            }
            default {
                Write-Host "    [X] " -ForegroundColor Red -NoNewline
                Write-Host "$relativePath - $($result.Message)" -ForegroundColor Red
                $invalidCount++
            }
        }
    }

    Write-Host ""
    Write-Host "  Summary:" -ForegroundColor Cyan
    Write-Host "    Valid signatures:   $validCount" -ForegroundColor Green
    Write-Host "    Not signed:         $notSignedCount" -ForegroundColor DarkGray
    Write-Host "    Invalid/Error:      $invalidCount" -ForegroundColor Red
    Write-Host ""

} else {
    # Signing mode
    $cert = Get-OrCreateCodeSigningCert -Thumbprint $CertificateThumbprint

    Write-Host ""
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    SIGNING SCRIPTS" -ForegroundColor White
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""

    $signedCount = 0
    $skippedCount = 0
    $errorCount = 0

    foreach ($script in $scripts) {
        $relativePath = $script.FullName.Replace($ScriptsPath, "").TrimStart("\")

        # Check if already signed with same certificate
        $existingSig = Get-AuthenticodeSignature -FilePath $script.FullName
        if ($existingSig.Status -eq "Valid" -and
            $existingSig.SignerCertificate.Thumbprint -eq $cert.Thumbprint) {
            Write-Host "    [--] " -ForegroundColor DarkGray -NoNewline
            Write-Host "$relativePath - Already signed" -ForegroundColor DarkGray
            $skippedCount++
            continue
        }

        $result = Sign-PowerShellScript -Certificate $cert -Script $script

        switch ($result.Status) {
            "Signed" {
                Write-Host "    [OK] " -ForegroundColor Green -NoNewline
                Write-Host $relativePath -ForegroundColor White
                $signedCount++
            }
            "Warning" {
                Write-Host "    [!] " -ForegroundColor Yellow -NoNewline
                Write-Host "$relativePath - $($result.Message)" -ForegroundColor Yellow
                $signedCount++
            }
            default {
                Write-Host "    [X] " -ForegroundColor Red -NoNewline
                Write-Host "$relativePath - $($result.Message)" -ForegroundColor Red
                $errorCount++
            }
        }
    }

    Write-Host ""
    Write-Host "  Summary:" -ForegroundColor Cyan
    Write-Host "    Signed:    $signedCount" -ForegroundColor Green
    Write-Host "    Skipped:   $skippedCount" -ForegroundColor DarkGray
    Write-Host "    Errors:    $errorCount" -ForegroundColor Red
    Write-Host ""

    # Instructions for trusting the certificate
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    TRUSTING THE CERTIFICATE" -ForegroundColor White
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  To trust the self-signed certificate on this machine:" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "    1. Open PowerShell as Administrator" -ForegroundColor White
    Write-Host "    2. Run:" -ForegroundColor White
    Write-Host ""
    Write-Host "       Import-Certificate -FilePath `"$ScriptsPath\scripts\windows\SpiralPool-CodeSigning.cer`" ``" -ForegroundColor Yellow
    Write-Host "           -CertStoreLocation Cert:\LocalMachine\TrustedPublisher" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  To verify scripts before running:" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "       Get-AuthenticodeSignature .\install-windows.ps1" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Certificate Thumbprint: $($cert.Thumbprint)" -ForegroundColor DarkGray
    Write-Host ""
}

Write-Host "╔════════════════════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "║  Script signing complete                                                   ║" -ForegroundColor Cyan
Write-Host "╚════════════════════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

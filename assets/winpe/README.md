# WinPE assets — LICENSED CONTENT REQUIRED, NOT BUNDLED

Booting Windows via iPXE/wimboot needs licensed Microsoft media. This
project never downloads or ships Windows images; only templates live here.

Layout (aligned to the vendor's production chain in M7; template
bodies recovered in M8 — see docs/M8-REPORT.md §1 and evidence in
deplos-reversing/m8-evidence/):

  Served by this server:
    GET /winpe/boot.ipxe?mac=...          renders boot.ipxe.tmpl
    GET /api/v1/ipxe/win/winpeshl.ini     renders winpeshl.ini.tmpl
                                          (vendor-verbatim route)
    GET /winpe/autounattend.xml           renders autounattend.xml.tmpl
                                          (vendor template name:
                                          windows_autounattend.xml.tmpl;
                                          consumed in PE as
                                          X:\Windows\System32\autounattend.xml)
    GET /winpe/<file>                     static files you provide

  Files the operator must supply (licensed media / ADK):
    wimboot        — iPXE wimboot kernel (open source, from ipxe.org;
                     vendor serves it from a "static/wimboot/" sub-FS)
    BCD            — Boot Configuration Data for WinPE
    boot.sdi       — RAM disk image descriptor
    boot.wim       — WinPE boot image (Windows ADK / licensed media)
    install media  — vendor server layout: <root>/unpacked/<iso>/sources/
                     boot.wim + install.wim|install.esd; winpeshl mounts
                     the install share over SMB (net use Z:) and runs
                     setup.exe with the unattend file.

Chain (vendor, M8-verbatim): iPXE script loads wimboot + BCD +
boot.sdi + boot.wim + winpeshl.ini + install.bat + decryptor.exe (all
as initrds) -> winpeshl.exe reads winpeshl.ini, whose entire body is
"[LaunchApp] / AppPath = %SYSTEMDRIVE%\Windows\System32\install.bat"
-> install.bat (JIT-generated per task by the vendor server) waits
for network, maps the install share with
net use Z: "\\<server>\<share>" "<password>" /user:<user>, wipes the
target disk via a diskpart script, runs decryptor.exe to decrypt the
answer file to X:\Windows\System32\autounattend.xml, and starts
setup.exe. The share is provisioned by the vendor's SambaManager,
which appends a read-only share block to /etc/samba/smb.conf
(valid users pxeuser, force user root) and restarts Samba.

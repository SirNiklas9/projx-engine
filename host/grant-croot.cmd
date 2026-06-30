@echo off
REM ============================================================================
REM  ProjX Windows cage — one-time elevated setup (RUN AS ADMINISTRATOR, ONCE).
REM
REM  Grants AppContainers permission to TRAVERSE the drive root C:\ (the dir
REM  itself only, non-recursive). A caged Node app (claude) realpath()s its
REM  script path up to C:\ at startup; without this it dies with
REM  "EPERM: lstat 'C:\'". This is the minimal, documented one-time admin step
REM  (see PROJX-ENGINE-WINCAGE-PRODUCTIONIZE.md). The cage itself runs UNPRIVILEGED
REM  after this; nothing else needs admin.
REM
REM  S-1-15-2-1 = ALL APPLICATION PACKAGES (covers every AppContainer cage, so this
REM  is run once, not per-cage). (RX) with no (OI)(CI) = traverse/list of C:\ only,
REM  NOT recursive into its contents.
REM ============================================================================
icacls C:\ /grant "*S-1-15-2-1:(RX)"
if %ERRORLEVEL%==0 (
  echo.
  echo OK - AppContainers can now traverse C:\. Caged Node/claude can start.
) else (
  echo.
  echo FAILED ^(error %ERRORLEVEL%^) - are you running this as Administrator?
)
pause

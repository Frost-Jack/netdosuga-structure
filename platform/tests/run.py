"""Sequential acceptance runner; preserve each suite's reports and stop on first failure."""
import subprocess,sys
for suite in ('smoke.py','business.py','mvp.py'):
    result=subprocess.run([sys.executable,suite],check=False)
    if result.returncode:raise SystemExit(result.returncode)

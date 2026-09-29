#!/bin/sh
set -eu

if git ls-files --error-unmatch .env >/dev/null 2>&1; then
  echo '.env is tracked; refusing to continue' >&2
  exit 1
fi

if ! git check-ignore -q .env; then
  echo '.env is not ignored; refusing to continue' >&2
  exit 1
fi

if command -v gitleaks >/dev/null 2>&1; then
  exec gitleaks detect --no-banner --redact
fi

matches=$(git ls-files -z --cached --others --exclude-standard | \
  xargs -0 -r rg -l \
    -e 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY' \
    -e '^(BACKEND_API_KEY|REMNAWAVE_API_TOKEN|BEDOLAGA_API_KEY|QUOTA_DB_PASSWORD)=[^[:space:]]{8,}$' \
    -e 'Authorization:[[:space:]]*Bearer[[:space:]]+[A-Za-z0-9._-]{32,}' \
    -- || true)
if [ -n "$matches" ]; then
  echo 'possible secret material found in:' >&2
  printf '%s\n' "$matches" >&2
  exit 1
fi

echo 'gitleaks is not installed; fallback scan and ignore/tracking checks passed' >&2

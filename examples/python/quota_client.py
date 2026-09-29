"""Minimal server-side REST client; usable from a bot or any Python backend."""

import json
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class QuotaError(Exception):
    def __init__(self, status, code):
        self.status = status
        self.code = code
        super().__init__(f"remna-quota: {code} (HTTP {status})")


class _NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class QuotaClient:
    def __init__(self, base_url, api_key, timeout=95):
        parsed = urlsplit(base_url)
        if (parsed.scheme not in ("https", "http") or not parsed.hostname
                or parsed.username or parsed.password or parsed.query or parsed.fragment):
            raise ValueError("invalid API base URL")
        if len(api_key) < 32 or any(char.isspace() for char in api_key):
            raise ValueError("invalid server API key")
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._timeout = timeout
        self._opener = build_opener(_NoRedirects())

    def request(self, method, path, body=None, query=None):
        """path is an API path chosen by your backend, never a user-supplied URL."""
        if not path.startswith("/api/v1/") or "?" in path or "#" in path:
            raise ValueError("invalid API path")
        endpoint = self._base_url + path
        if query:
            endpoint += "?" + urlencode(query)
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = Request(endpoint, data=data, method=method, headers={
            "Authorization": "Bearer " + self._api_key,
            "Content-Type": "application/json", "Accept": "application/json",
        })
        try:
            with self._opener.open(req, timeout=self._timeout) as response:
                if response.status == 204:
                    return None
                data = response.read(4 * 1024 * 1024 + 1)
                if len(data) > 4 * 1024 * 1024:
                    raise QuotaError(response.status, "response_too_large")
                return json.loads(data)
        except HTTPError as error:
            status = error.code
            error.close()
            # Never put the request URL, headers or raw error body in exceptions.
            code = {401: "unauthorized", 404: "not_found", 409: "revision_conflict",
                    422: "invalid_bundle", 503: "source_unavailable"}.get(status, "http_error")
            raise QuotaError(status, code) from None
        except (URLError, TimeoutError, OSError, ValueError):
            raise QuotaError(0, "transport_or_response_error") from None

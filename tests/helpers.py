"""Shared helpers for the Oakestra E2E suite."""

import json
import time

import requests

# Statuses that mean the platform gave up on a deployment - polling further
# is pointless. Mirrors NegativeSchedulingStatus / DeploymentStatus in
# oakestra's oakestra_utils library.
FAILURE_STATUSES = {
    "TargetClusterNotFound",
    "TargetClusterNotActive",
    "TargetClusterNoCapacity",
    "NoActiveClusterWithCapacity",
    "NO_WORKER_CAPACITY",
    "NO_QUALIFIED_WORKER_FOUND",
    "NO_NODE_FOUND",
    "FAILED",
    "DEAD",
}


class TimeoutExpired(AssertionError):
    pass


def wait_until(check, timeout, interval=3, desc="condition"):
    """Poll `check` until it returns a truthy value, then return that value.

    Exceptions raised by `check` (e.g. connection refused while the stack is
    still booting) are swallowed and retried until the timeout expires.
    AssertionError is treated as a definitive failure and propagates
    immediately - raise it inside `check` to fail fast (see assert_not_failed).
    """
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
            last_error = None
        except AssertionError:
            raise
        except Exception as e:
            last_error = e
        time.sleep(interval)
    message = f"Timed out after {timeout}s waiting for {desc}"
    if last_error is not None:
        message += f" (last error: {last_error!r})"
    raise TimeoutExpired(message)


class ApiClient:
    """Thin wrapper around requests.Session with base URL and JWT handling.

    Re-logs in automatically when a request comes back 401 - the access
    token expires after ~15 minutes, which a slow E2E run can exceed.
    """

    def __init__(self, base_url):
        self.base_url = base_url.rstrip("/")
        self.session = requests.Session()
        self._credentials = None

    def login(self, username, password):
        resp = self.session.post(
            f"{self.base_url}/api/auth/login",
            json={"username": username, "password": password},
            timeout=10,
        )
        resp.raise_for_status()
        token = resp.json().get("token")
        if not token:
            raise AssertionError(f"login returned no token: {resp.text}")
        self.session.headers["Authorization"] = f"Bearer {token}"
        self._credentials = (username, password)
        return token

    def _request(self, method, path, **kwargs):
        kwargs.setdefault("timeout", 15)
        resp = self.session.request(method, self.base_url + path, **kwargs)
        if resp.status_code == 401 and self._credentials:
            self.login(*self._credentials)
            resp = self.session.request(method, self.base_url + path, **kwargs)
        return resp

    def get(self, path, **kwargs):
        return self._request("GET", path, **kwargs)

    def post(self, path, **kwargs):
        return self._request("POST", path, **kwargs)

    def delete(self, path, **kwargs):
        return self._request("DELETE", path, **kwargs)


def json_body(resp):
    """Parse a response body, unwrapping double-encoded JSON.

    Several system_manager endpoints return json_util.dumps(...) through
    flask-smorest, which serializes the already-serialized string again -
    the body is then a JSON string containing JSON.
    """
    data = resp.json()
    if isinstance(data, str):
        data = json.loads(data)
    return data


def object_id(doc):
    """Extract a document id whether it is a plain string or {"$oid": ...}."""
    oid = doc.get("_id")
    if isinstance(oid, dict):
        return oid.get("$oid")
    return str(oid)


def build_sla(app_name, microservices):
    """Build a minimal SLA document accepted by POST /api/application/."""
    return {
        "sla_version": "v2.0",
        "customerID": "Admin",
        "applications": [
            {
                "applicationID": "",
                "application_name": app_name,
                "application_namespace": "test",
                "application_desc": "oakestra-macos-testing E2E app",
                "microservices": microservices,
            }
        ],
    }


def build_microservice(name, image="docker.io/library/nginx:latest", cmd=None):
    """Build a minimal container microservice for an SLA document."""
    return {
        "microserviceID": "",
        "microservice_name": name,
        "microservice_namespace": "test",
        "virtualization": "container",
        "cmd": cmd or [],
        "memory": 100,
        "vcpus": 1,
        "vgpus": 0,
        "vtpus": 0,
        "bandwidth_in": 0,
        "bandwidth_out": 0,
        "storage": 0,
        "code": image,
        "state": "",
        "port": "",
        "added_files": [],
        "constraints": [],
    }


def get_service(client, service_id):
    """Fetch a service/job document, or None if it does not exist."""
    resp = client.get(f"/api/service/{service_id}")
    if resp.status_code == 404:
        return None
    resp.raise_for_status()
    return json_body(resp)


def running_instances(job):
    """Return the list of instances of a job that report RUNNING."""
    instances = job.get("instance_list") or []
    return [i for i in instances if i.get("status") == "RUNNING"]


def assert_not_failed(job):
    """Fail fast if the scheduler or worker gave up on the job."""
    status = job.get("status")
    if status in FAILURE_STATUSES:
        raise AssertionError(
            f"job {job.get('job_name')} entered failure state {status}: "
            f"{job.get('status_detail', 'no detail')}"
        )

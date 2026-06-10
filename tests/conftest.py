import os

import pytest
from helpers import ApiClient, json_body, wait_until

ROOT_API = os.environ.get("OAK_ROOT_API", "http://localhost:10000")
CLUSTER_API = os.environ.get("OAK_CLUSTER_API", "http://localhost:10100")
ROOT_RESOURCE_ABSTRACTOR = os.environ.get("OAK_ROOT_RA", "http://localhost:11011")
USERNAME = os.environ.get("OAK_USERNAME", "Admin")
PASSWORD = os.environ.get("OAK_PASSWORD", "Admin")

# How long to wait for the stack to boot / the cluster+worker to register
READY_TIMEOUT = int(os.environ.get("OAK_READY_TIMEOUT", "180"))
# How long to wait for a service instance to reach RUNNING (includes image pull)
DEPLOY_TIMEOUT = int(os.environ.get("OAK_DEPLOY_TIMEOUT", "300"))


@pytest.fixture(scope="session")
def config():
    return {
        "root_api": ROOT_API,
        "cluster_api": CLUSTER_API,
        "root_resource_abstractor": ROOT_RESOURCE_ABSTRACTOR,
        "ready_timeout": READY_TIMEOUT,
        "deploy_timeout": DEPLOY_TIMEOUT,
    }


@pytest.fixture(scope="session")
def root_api(config):
    """Logged-in client for the system_manager API.

    Retries the login while the stack is still booting so 'make e2e' can run
    the suite immediately after 'make up'.
    """
    client = ApiClient(config["root_api"])

    def try_login():
        client.login(USERNAME, PASSWORD)
        return client

    return wait_until(
        try_login,
        timeout=config["ready_timeout"],
        desc=f"login at {config['root_api']} (is the stack up? try 'make up')",
    )


@pytest.fixture(scope="session")
def active_cluster(root_api, config):
    """Wait until at least one cluster is registered and active at the root.

    A cluster only turns active once a worker is attached and resource
    reports flow, so this implicitly waits for the worker too.
    """

    def first_active():
        resp = root_api.get("/api/clusters/active")
        resp.raise_for_status()
        clusters = json_body(resp)
        return clusters[0] if clusters else None

    return wait_until(
        first_active,
        timeout=config["ready_timeout"],
        desc="an active cluster (worker attached and reporting)",
    )

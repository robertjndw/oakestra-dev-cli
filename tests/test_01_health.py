"""Infrastructure health: every entry point of the stack answers."""

import requests
from helpers import wait_until


def test_system_manager_reachable(config):
    """The root API answers HTTP within the ready timeout (handles cold boot)."""

    def root_answers():
        resp = requests.get(f"{config['root_api']}/api/clusters/", timeout=5)
        return resp.status_code == 200

    wait_until(
        root_answers,
        timeout=config["ready_timeout"],
        desc=f"system_manager at {config['root_api']}",
    )


def test_swagger_docs_available(config):
    resp = requests.get(f"{config['root_api']}/api/docs", timeout=10)
    assert resp.status_code == 200


def test_cluster_manager_reachable(config):
    """The cluster manager answers HTTP (any status code counts as alive)."""

    def cluster_answers():
        requests.get(f"{config['cluster_api']}/", timeout=5)
        return True

    wait_until(
        cluster_answers,
        timeout=config["ready_timeout"],
        desc=f"cluster_manager at {config['cluster_api']}",
    )


def test_admin_login(root_api):
    """The root_api fixture performed the login; the token must be in place."""
    assert root_api.session.headers.get("Authorization", "").startswith("Bearer ")

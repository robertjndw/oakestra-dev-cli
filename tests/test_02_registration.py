"""Cluster and worker registration: the hierarchy is wired up."""

import requests
from helpers import wait_until


def _cluster_candidates(config):
    """Raw cluster candidate documents stored at the root resource abstractor."""
    resp = requests.get(f"{config['root_resource_abstractor']}/api/v1/resources/", timeout=5)
    resp.raise_for_status()
    return resp.json()


def test_cluster_registered_and_active(active_cluster):
    assert active_cluster.get("active") is True


def test_worker_attached(config, active_cluster):
    """The cluster reports at least one active worker node to the root.

    cluster_manager aggregates its workers and pushes the result (including
    'active_nodes') to the root every 15 seconds.
    """

    def cluster_reports_nodes():
        for candidate in _cluster_candidates(config):
            if int(candidate.get("active_nodes") or 0) >= 1:
                return candidate
        return None

    candidate = wait_until(
        cluster_reports_nodes,
        timeout=config["ready_timeout"],
        desc="a cluster reporting >= 1 active worker node",
    )
    assert int(candidate["active_nodes"]) >= 1


def test_cluster_reports_resources(config, active_cluster):
    """The aggregated worker resources (cpu/memory) reach the root."""

    def cluster_reports_capacity():
        for candidate in _cluster_candidates(config):
            memory = float(candidate.get("memory") or 0)
            vcpus = float(candidate.get("vcpus") or 0)
            if memory > 0 and vcpus > 0:
                return candidate
        return None

    wait_until(
        cluster_reports_capacity,
        timeout=config["ready_timeout"],
        desc="a cluster reporting nonzero cpu/memory capacity",
    )

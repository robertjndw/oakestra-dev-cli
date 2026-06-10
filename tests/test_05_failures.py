"""Negative paths: the platform must reject and report what it cannot run.

These tests verify failure REPORTING, so unlike the happy-path tests they
must not fail fast on failure statuses - the failure status is the
expected outcome.
"""

import time

import pytest
from helpers import (
    NEGATIVE_SCHEDULING_STATUSES,
    build_microservice,
    get_service,
    register_app,
    undeploy_all_instances,
    wait_until,
)

pytestmark = pytest.mark.deployment


def _deploy_and_watch(root_api, app_name, microservice, check, timeout, desc):
    """Register a single-service app, deploy it, poll `check(job)`, clean up."""
    app_id, service_ids = register_app(root_api, app_name, [microservice])
    service_id = next(iter(service_ids.values()))
    try:
        resp = root_api.post(f"/api/service/{service_id}/instance")
        assert resp.status_code == 200, f"deploy failed: {resp.text}"

        def job_check():
            job = get_service(root_api, service_id)
            assert job is not None, "service disappeared while waiting"
            return check(job)

        return wait_until(job_check, timeout=timeout, interval=5, desc=desc)
    finally:
        undeploy_all_instances(root_api, service_id)
        root_api.delete(f"/api/application/{app_id}")


def test_oversized_request_rejected_by_scheduler(root_api, active_cluster, config):
    """A service requesting more memory than the whole cluster has must be
    rejected with a negative scheduling status, not sit in REQUESTED forever."""

    def rejected(job):
        statuses = {i.get("status") for i in job.get("instance_list") or []}
        statuses.add(job.get("status"))
        return statuses & NEGATIVE_SCHEDULING_STATUSES

    result = _deploy_and_watch(
        root_api,
        f"e2ebig{int(time.time()) % 1000000}",
        build_microservice("hugemem", memory=10_000_000),  # 10 TB
        rejected,
        timeout=config["ready_timeout"],
        desc="scheduler to reject a 10TB-memory service with a capacity status",
    )
    assert result & NEGATIVE_SCHEDULING_STATUSES


def test_unpullable_image_reports_failed(root_api, active_cluster, config):
    """A nonexistent image must surface as FAILED at the root, with a
    status detail explaining what went wrong on the worker."""

    def failed_with_detail(job):
        instances = job.get("instance_list") or []
        for instance in instances:
            if instance.get("status") == "FAILED":
                detail = instance.get("status_detail") or job.get("status_detail") or ""
                assert detail.strip(), "FAILED status arrived without any status_detail"
                return instance
        if job.get("status") == "FAILED":
            assert (job.get("status_detail") or "").strip(), (
                "FAILED status arrived without any status_detail"
            )
            return job
        return None

    _deploy_and_watch(
        root_api,
        f"e2ebadimg{int(time.time()) % 1000000}",
        build_microservice(
            "badimage", image="docker.io/library/oakestra-e2e-does-not-exist:latest"
        ),
        failed_with_detail,
        timeout=config["deploy_timeout"],
        desc="a nonexistent image to be reported FAILED with detail",
    )

"""Full deployment lifecycle: SLA -> scheduled -> RUNNING -> scale -> undeploy.

The tests in this module share one application (module-scoped fixture) and
run in file order: deploy, scale up, scale down, undeploy + delete.
"""

import time

import pytest
from helpers import (
    assert_not_failed,
    build_microservice,
    get_service,
    register_app,
    running_instances,
    wait_until,
)

pytestmark = pytest.mark.deployment


@pytest.fixture(scope="module")
def app(root_api, active_cluster):
    """Register a test application with one nginx microservice, clean up after."""
    app_name = f"e2e{int(time.time()) % 1000000}"
    app_id, service_ids = register_app(root_api, app_name, [build_microservice("nginx")])
    service_id = service_ids["nginx"]

    yield {"id": app_id, "name": app_name, "service_id": service_id}

    # Cleanup is best-effort: the last test already deletes the app when the
    # whole module passes, so this mostly covers mid-module failures.
    root_api.delete(f"/api/application/{app_id}")


def _wait_for_running(root_api, service_id, count, timeout):
    """Wait until exactly `count` instances of the service report RUNNING."""

    def has_running_instances():
        job = get_service(root_api, service_id)
        assert job is not None, "service disappeared while waiting"
        assert_not_failed(job)
        return len(running_instances(job)) == count

    wait_until(
        has_running_instances,
        timeout=timeout,
        interval=5,
        desc=f"{count} RUNNING instance(s) of service {service_id}",
    )


def test_deploy_service_reaches_running(root_api, app, config):
    resp = root_api.post(f"/api/service/{app['service_id']}/instance")
    assert resp.status_code == 200, f"deploy failed: {resp.text}"

    _wait_for_running(root_api, app["service_id"], 1, config["deploy_timeout"])


def test_scale_up_to_two_instances(root_api, app, config):
    resp = root_api.post(f"/api/service/{app['service_id']}/instance")
    assert resp.status_code == 200, f"scale up failed: {resp.text}"

    _wait_for_running(root_api, app["service_id"], 2, config["deploy_timeout"])


def test_scale_down_to_one_instance(root_api, app, config):
    job = get_service(root_api, app["service_id"])
    instances = job.get("instance_list") or []
    assert len(instances) == 2, f"expected 2 instances before scale down: {instances}"

    highest = max(int(i["instance_number"]) for i in instances)
    resp = root_api.delete(f"/api/service/{app['service_id']}/instance/{highest}")
    assert resp.status_code == 200, f"scale down failed: {resp.text}"

    _wait_for_running(root_api, app["service_id"], 1, config["deploy_timeout"])


def test_undeploy_and_delete_application(root_api, app, config):
    # Undeploy the remaining instance
    job = get_service(root_api, app["service_id"])
    for instance in job.get("instance_list") or []:
        resp = root_api.delete(
            f"/api/service/{app['service_id']}/instance/{instance['instance_number']}"
        )
        assert resp.status_code == 200, f"undeploy failed: {resp.text}"

    def no_instances_left():
        current = get_service(root_api, app["service_id"])
        assert current is not None, "service disappeared before app deletion"
        return len(current.get("instance_list") or []) == 0

    wait_until(
        no_instances_left,
        timeout=config["deploy_timeout"],
        interval=5,
        desc="all instances undeployed",
    )

    # Delete the application (removes its services as well)
    resp = root_api.delete(f"/api/application/{app['id']}")
    assert resp.status_code == 200, f"app deletion failed: {resp.text}"

    def service_gone():
        return get_service(root_api, app["service_id"]) is None

    wait_until(service_gone, timeout=60, interval=3, desc="service removed with the app")

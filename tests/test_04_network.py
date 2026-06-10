"""Overlay networking: service-to-service traffic via the semantic service IP.

Deploys an nginx 'web' service with a fixed round-robin service IP, then a
one-shot busybox 'client' that wgets that IP from inside its own container.
The client exits 0 only if the HTTP request succeeds, and NodeEngine reports
one-shot jobs that exit 0 as COMPLETED - so a COMPLETED client proves the
whole NetManager data path worked: client netns -> proxy tun -> service IP
translation -> web instance.
"""

import time

import pytest
from helpers import (
    assert_not_failed,
    build_microservice,
    get_service,
    register_app,
    running_instances,
    undeploy_all_instances,
    wait_until,
)

pytestmark = pytest.mark.deployment

# Round-robin service IP from Oakestra's 10.30.0.0/16 service range,
# requested explicitly in the SLA so the client knows it upfront.
WEB_RR_IP = "10.30.30.30"

CLIENT_CMD = [
    "sh",
    "-c",
    # Retry for ~60s to absorb the web instance's startup, then verdict
    f"for i in $(seq 20); do wget -q -O- -T 3 http://{WEB_RR_IP} && exit 0; sleep 3; done; exit 1",
]


@pytest.fixture(scope="module")
def net_app(root_api, active_cluster):
    app_name = f"e2enet{int(time.time()) % 1000000}"
    microservices = [
        build_microservice("web", addresses={"rr_ip": WEB_RR_IP}),
        build_microservice(
            "client",
            image="docker.io/library/busybox:latest",
            cmd=CLIENT_CMD,
            one_shot=True,
        ),
    ]
    app_id, service_ids = register_app(root_api, app_name, microservices)

    yield {"id": app_id, "services": service_ids}

    for service_id in service_ids.values():
        undeploy_all_instances(root_api, service_id)
    root_api.delete(f"/api/application/{app_id}")


def test_web_running_with_service_ip(root_api, net_app, config):
    web_id = net_app["services"]["web"]
    resp = root_api.post(f"/api/service/{web_id}/instance")
    assert resp.status_code == 200, f"deploy failed: {resp.text}"

    def web_running():
        job = get_service(root_api, web_id)
        assert job is not None, "web service disappeared"
        assert_not_failed(job)
        return len(running_instances(job)) == 1

    wait_until(
        web_running,
        timeout=config["deploy_timeout"],
        interval=5,
        desc="web instance RUNNING",
    )

    job = get_service(root_api, web_id)
    assert job.get("RR_ip") == WEB_RR_IP, f"requested service IP not set: {job.get('RR_ip')}"


def test_client_reaches_web_over_overlay(root_api, net_app, config):
    client_id = net_app["services"]["client"]
    resp = root_api.post(f"/api/service/{client_id}/instance")
    assert resp.status_code == 200, f"deploy failed: {resp.text}"

    def client_completed():
        job = get_service(root_api, client_id)
        assert job is not None, "client service disappeared"
        # FAILED/DEAD here means the wget loop exhausted its retries -
        # the overlay did not deliver traffic to the web service.
        assert_not_failed(job)
        statuses = {i.get("status") for i in job.get("instance_list") or []}
        statuses.add(job.get("status"))
        return "COMPLETED" in statuses

    wait_until(
        client_completed,
        timeout=config["deploy_timeout"],
        interval=5,
        desc=f"client one-shot wget against {WEB_RR_IP} to COMPLETE over the overlay",
    )

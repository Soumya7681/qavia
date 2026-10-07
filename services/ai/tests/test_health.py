from fastapi.testclient import TestClient

from src.main import CORRELATION_HEADER, app

client = TestClient(app)


def test_healthz() -> None:
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json()["status"] == "ok"


def test_correlation_id_is_echoed() -> None:
    response = client.get("/healthz", headers={CORRELATION_HEADER: "abc-123"})
    assert response.headers[CORRELATION_HEADER] == "abc-123"


def test_service_has_no_database_dependency() -> None:
    """Stateless is enforced by not having the capability, not by convention."""
    import tomllib
    from pathlib import Path

    pyproject = tomllib.loads(Path(__file__).parent.parent.joinpath("pyproject.toml").read_text())
    declared = " ".join(pyproject["project"]["dependencies"]).lower()
    for banned in ("psycopg", "asyncpg", "sqlalchemy", "alembic", "databases"):
        assert banned not in declared, f"AI service must not depend on {banned}"

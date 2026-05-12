use axum::body::Body;
use cine_daemon::{app, DaemonState};
use http::{header::AUTHORIZATION, Request, StatusCode};
use tower::ServiceExt;

#[tokio::test]
async fn health_requires_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .oneshot(
            Request::builder()
                .uri("/health")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
}

#[tokio::test]
async fn health_returns_daemon_status_with_valid_token() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .oneshot(
            Request::builder()
                .uri("/health")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);

    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: serde_json::Value = serde_json::from_slice(&bytes).unwrap();

    assert_eq!(payload["service"], "cine-daemon");
    assert_eq!(payload["status"], "ok");
    assert_eq!(payload["schema"]["status"], "unchecked");
    assert_eq!(payload["database"]["configured"], false);
}

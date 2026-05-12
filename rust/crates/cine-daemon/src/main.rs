#[tokio::main]
async fn main() -> anyhow::Result<()> {
    cine_daemon::serve_from_env().await
}

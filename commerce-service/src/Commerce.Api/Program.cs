using Commerce.Api.Background;
using Commerce.Api.Clients;
using Commerce.Api.Grpc;
using Commerce.Api.Observability;
using Commerce.Application.Handlers;
using Commerce.Application.Shop;
using Commerce.Domain.Ports;
using Commerce.Infrastructure.Clients;
using Commerce.Infrastructure.Messaging;
using Commerce.Infrastructure.Observability;
using Commerce.Infrastructure.Persistence;
using Commerce.Infrastructure.Persistence.Repositories;
using Microsoft.AspNetCore.Server.Kestrel.Core;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Console;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;
using Prometheus;

var builder = WebApplication.CreateBuilder(args);

builder.Logging.ClearProviders();
builder.Logging.AddConsole(options => options.FormatterName = "commerce-json");
builder.Logging.AddConsoleFormatter<CommerceJsonConsoleFormatter, ConsoleFormatterOptions>();

var connectionString = builder.Configuration.GetConnectionString("Commerce")
    ?? "Host=localhost;Port=5432;Database=demo;Username=demo;Password=demo";
var identityUrl = builder.Configuration["IDENTITY_BASE_URL"] ?? "http://identity-service:8084";
var paymentUrl = builder.Configuration["PAYMENT_BASE_URL"] ?? "http://payment-mock:8085";
var otlpEndpoint = builder.Configuration["OTEL_EXPORTER_OTLP_ENDPOINT"] ?? "http://jaeger:4317";
if (!otlpEndpoint.Contains("://", StringComparison.Ordinal))
{
    otlpEndpoint = "http://" + otlpEndpoint;
}

builder.WebHost.ConfigureKestrel(options =>
{
    options.ListenAnyIP(50051, listen => listen.Protocols = HttpProtocols.Http2);
    options.ListenAnyIP(8087, listen => listen.Protocols = HttpProtocols.Http1AndHttp2);
});

builder.Services.AddDbContext<CommerceDbContext>(options => options.UseNpgsql(connectionString));
builder.Services.AddGrpc();
builder.Services.AddGrpcHealthChecks();
builder.Services.AddOpenApi();
builder.Services.AddHttpClient("identity", client => client.Timeout = TimeSpan.FromSeconds(2));
builder.Services.AddHttpClient("payment", client => client.Timeout = TimeSpan.FromSeconds(1));

builder.Services.AddSingleton<IClock, SystemClock>();
builder.Services.AddSingleton<IIdentifierGenerator, UuidGenerator>();
builder.Services.AddScoped<IApplicationRepository, ApplicationRepository>();
builder.Services.AddScoped<IOfferRepository, OfferRepository>();
builder.Services.AddScoped<ICartRepository, CartRepository>();
builder.Services.AddScoped<IOrderRepository, OrderRepository>();
builder.Services.AddScoped<IOutboxRepository, OutboxRepository>();
builder.Services.AddScoped<IAttemptRepository, AttemptRepository>();
builder.Services.AddScoped<IUnitOfWork, UnitOfWork>();
builder.Services.AddScoped<IDomainEventWriter, DomainEventWriter>();
builder.Services.AddScoped<IOfferRatingReader, OfferRatingReader>();
var pricingAddress = builder.Configuration["PRICING_GRPC_ADDR"];
if (string.IsNullOrWhiteSpace(pricingAddress))
{
    builder.Services.AddScoped<IPricingClient, BasePricePricingClient>();
}
else
{
    builder.Services.AddSingleton<IPricingClient>(new GrpcPricingClient(pricingAddress));
}
builder.Services.AddScoped<IActorResolver>(provider =>
    new IdentityClient(provider.GetRequiredService<IHttpClientFactory>().CreateClient("identity"), identityUrl));
builder.Services.AddScoped<IPaymentClient>(provider =>
    new PaymentMockClient(provider.GetRequiredService<IHttpClientFactory>().CreateClient("payment"), paymentUrl));

builder.Services.AddScoped<IRequestHandler, AdminHandler>();
builder.Services.AddScoped<IRequestHandler, CatalogHandler>();
builder.Services.AddScoped<IRequestHandler, ApplicationHandler>();
builder.Services.AddScoped<IRequestHandler, OfferHandler>();
builder.Services.AddScoped<IRequestHandler, CartHandler>();
builder.Services.AddScoped<IRequestHandler, OrderHandler>();
builder.Services.AddScoped<IRequestHandler, TicketHandler>();
builder.Services.AddScoped<IRequestHandler, SalesHandler>();
builder.Services.AddScoped<ShopDispatcher>();
builder.Services.AddScoped<ReservationExpiryService>();

builder.Services.AddHostedService<ConfigurationGuard>();
builder.Services.AddHostedService<StartupMigrationService>();
builder.Services.AddHostedService<ReservationExpiryWorker>();
builder.Services.AddHostedService<OutboxPublisher>();

builder.Services.AddOpenTelemetry()
    .WithTracing(tracing => tracing
        .SetResourceBuilder(ResourceBuilder.CreateDefault().AddService(
            builder.Configuration["OTEL_SERVICE_NAME"] ?? "commerce-service"))
        .AddSource(Telemetry.Source.Name)
        .AddSource(InfrastructureTelemetry.Source.Name)
        .AddAspNetCoreInstrumentation()
        .AddHttpClientInstrumentation()
        .AddOtlpExporter(options => options.Endpoint = new Uri(otlpEndpoint)));

var app = builder.Build();

app.MapMetrics();
app.MapOpenApi();
app.MapGrpcService<ShopGrpcService>();
app.MapGrpcHealthChecksService();
app.MapPut("/internal/offers/{id}/photo", async (string id, HttpRequest request, IServiceProvider services, IConfiguration configuration, CancellationToken cancellationToken) =>
{
    var expected = configuration["COMMERCE_INTERNAL_TOKEN"];
    if (string.IsNullOrWhiteSpace(expected) || request.Headers["X-Internal-Token"].ToString() != expected)
    {
        return Results.Unauthorized();
    }

    var binding = await request.ReadFromJsonAsync<PhotoBinding>(cancellationToken);
    if (binding is null || !binding.PhotoPath.StartsWith("/photos/", StringComparison.Ordinal) || binding.PhotoPath.Contains("..", StringComparison.Ordinal))
    {
        return Results.BadRequest();
    }

    using var scope = services.CreateScope();
    var repository = scope.ServiceProvider.GetRequiredService<IOfferRepository>();
    await repository.SetPhotoPathAsync(id, binding.PhotoPath, cancellationToken);
    await scope.ServiceProvider.GetRequiredService<CommerceDbContext>().SaveChangesAsync(cancellationToken);
    return Results.NoContent();
});
app.MapGet("/healthz", () => Results.Text("ok", "text/plain"));
app.MapGet("/readyz", async (IServiceProvider services, CancellationToken cancellationToken) =>
{
    try
    {
        using var scope = services.CreateScope();
        var db = scope.ServiceProvider.GetRequiredService<CommerceDbContext>();
        await db.Database.ExecuteSqlRawAsync("SELECT 1", cancellationToken);
        return Results.Text("ready", "text/plain");
    }
    catch
    {
        return Results.StatusCode(StatusCodes.Status503ServiceUnavailable);
    }
});

await app.RunAsync();

public sealed record PhotoBinding(string PhotoPath);

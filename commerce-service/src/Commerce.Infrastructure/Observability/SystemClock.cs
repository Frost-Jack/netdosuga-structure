using System.Diagnostics;
using Commerce.Domain.Ports;

namespace Commerce.Infrastructure.Observability;

public static class InfrastructureTelemetry
{
    public static readonly ActivitySource Source = new("Commerce.Infrastructure", "1.0.0");
}

public sealed class SystemClock : IClock
{
    public DateTimeOffset UtcNow => DateTimeOffset.UtcNow;
}

public sealed class UuidGenerator : IIdentifierGenerator
{
    public string NewId() => Guid.NewGuid().ToString("D");
}

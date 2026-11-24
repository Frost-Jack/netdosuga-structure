namespace Commerce.Domain.Ports;

public interface IDomainEventWriter
{
    Task WriteAsync(string eventType, string orderId, CancellationToken cancellationToken);
}

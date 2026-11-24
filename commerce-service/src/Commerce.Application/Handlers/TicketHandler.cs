using Commerce.Application.Shop;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class TicketHandler(
    IOrderRepository orders,
    IAttemptRepository attempts,
    IUnitOfWork unitOfWork,
    IDomainEventWriter events) : IRequestHandler
{
    public bool CanHandle(string operation) => operation == "tickets.redeem";

    public Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsVendor)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        return unitOfWork.ExecuteAsync(async token =>
        {
            var ticket = await orders.GetTicketByCodeAsync(context.Id, token)
                ?? throw new DomainException(DomainError.NotFound());
            if (ticket.VendorId != context.Actor.Id)
            {
                throw new DomainException(DomainError.Forbidden());
            }

            var order = await orders.GetAsync(ticket.OrderId, token)
                ?? throw new DomainException(DomainError.NotFound());
            if (await attempts.GetRefundAttemptAsync(order.Id, token) is not null)
            {
                throw new DomainException(DomainError.Conflict("REFUND_PENDING"));
            }

            if (ticket.Status != TicketStatus.Valid || order.Status != OrderStatus.Paid)
            {
                throw new DomainException(DomainError.Conflict("INVALID_TICKET_STATE"));
            }

            ticket.Status = TicketStatus.Redeemed;
            await events.WriteAsync("ticket.redeemed", order.Id, token);
            return ShopOutcome.Ok(new Dictionary<string, object?>
            {
                ["ticket"] = EntityMapper.ToDictionary(ticket)
            });
        }, cancellationToken: cancellationToken);
    }
}

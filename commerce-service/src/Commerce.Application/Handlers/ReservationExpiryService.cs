using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class ReservationExpiryService(
    IOrderRepository orders,
    IOfferRepository offers,
    IAttemptRepository attempts,
    IUnitOfWork unitOfWork,
    IPaymentClient payment,
    IDomainEventWriter events)
{
    public async Task<bool> ExpireOnceAsync(CancellationToken cancellationToken)
    {
        var expired = await orders.ListExpiredAsync(1, cancellationToken);
        if (expired.Count == 0)
        {
            return false;
        }

        await unitOfWork.ExecuteAsync(async token =>
        {
            var order = expired[0];
            if (order.Status != OrderStatus.Reserved)
            {
                return true;
            }

            if (await attempts.HasPaymentAttemptAsync(order.Id, token))
            {
                await payment.CancelAsync(order.Id, order.TotalCents, token);
            }

            var items = await orders.ListItemsAsync(order.Id, token);
            foreach (var item in items)
            {
                var offer = await offers.GetAsync(item.OfferId, token);
                offer?.Release(item.Quantity);
            }

            order.Status = OrderStatus.Expired;
            await events.WriteAsync("order.expired", order.Id, token);
            return true;
        }, cancellationToken: cancellationToken);
        return true;
    }
}

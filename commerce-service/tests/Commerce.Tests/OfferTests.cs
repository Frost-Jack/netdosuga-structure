using Commerce.Domain;
using Commerce.Domain.Entities;
using Xunit;

namespace Commerce.Tests;

public sealed class OfferTests
{
    [Fact]
    public void Reserve_ReducesAvailable()
    {
        var offer = new Offer { Capacity = 10, Available = 10 };

        offer.Reserve(3);

        Assert.Equal(7, offer.Available);
    }

    [Fact]
    public void Reserve_WhenNotEnough_ThrowsConflict()
    {
        var offer = new Offer { Capacity = 10, Available = 2 };

        var error = Assert.Throws<DomainException>(() => offer.Reserve(3));

        Assert.Equal(409, error.Error.Status);
        Assert.Equal("UNAVAILABLE_CAPACITY", error.Error.Code);
    }

    [Fact]
    public void Release_NeverExceedsCapacity()
    {
        var offer = new Offer { Capacity = 10, Available = 9 };

        offer.Release(5);

        Assert.Equal(10, offer.Available);
    }
}
